package cafehunter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/sheets/v4"

	"github.com/TomiHiltunen/geohash-golang"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/urlfetch"
)

const (
	BOT_TOKEN       = ""
	PAGE_TOKEN      = ""
	GOOG_MAP_APIKEY = ""

	FBMessageURI = "https://graph.facebook.com/v2.6/me/messages?access_token=" + PAGE_TOKEN
	WELCOME_TEXT = `你好，歡迎使用 Café Hunter。請用簡單的句子跟我對話，例如：「我要找咖啡店」、「我想喝咖啡」、「士林有什麼推薦的咖啡店嗎？」`
)

var lock sync.Mutex = sync.Mutex{}

type FBObject struct {
	Object string
	Entry  []FBEntry
}

type FBEntry struct {
	Id        string
	Time      int64
	Messaging []FBMessage
}

type FBSender struct {
	Id int64 `json:"id,string"`
}

type FBRecipient struct {
	Id int64 `json:"id,string"`
}

type FBMessage struct {
	Sender    FBSender           `json:"sender,omitempty"`
	Recipient FBRecipient        `json:"recipient,omitempty"`
	Timestamp int64              `json:"timestamp,omitempty"`
	Content   *FBMessageContent  `json:"message,omitempty"`
	Delivery  *FBMessageDelivery `json:"delivery,omitempty"`
	Postback  *FBMessagePostback `json:"postback,omitempty"`
}

type FBMessageQuickReply struct {
	Payload string
}

type FBMessageContent struct {
	Text        string                `json:"text"`
	Seq         int64                 `json:"seq,omitempty"`
	Attachments []FBMessageAttachment `json:"attachments,omitempty"`
	QuickReplay *FBMessageQuickReply  `json:"quick_reply,omitempty"`
}

type FBMessageAttachment struct {
	Title   string          `json:",omitempty"`
	Url     string          `json:",omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type FBLocationAttachment struct {
	Coordinates Location `json:"coordinates"`
}

type Location struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"long"`
}

type FBMessageTemplate struct {
	Type     string          `json:"template_type"`
	Elements json.RawMessage `json:"elements"`
}

type FBButtonItem struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Url     string `json:"url,omitempty"`
	Payload string `json:"payload,omitempty"`
}

type FBMessageDelivery struct {
	Watermark int64 `json:"watermark"`
	Seq       int64 `json:"seq"`
}

type FBMessagePostback struct {
	Payload string `json:"payload"`
}

type User struct {
	Id         int64
	TodoAction string
	LastText   string
}

var users map[int64]*User = map[int64]*User{}
var cafes []Cafe

func init() {
	http.HandleFunc("/fbCallback", fbCBHandler)
	http.HandleFunc("/", handler)
}

func loadCafeData(ctx context.Context) (cafes []Cafe, err error) {
	lock.Lock()
	defer lock.Unlock()

	client, err := google.DefaultClient(ctx, sheets.SpreadsheetsReadonlyScope)
	if err != nil {
		log.Errorf(ctx, "fail to establish google client: %+v", err)
		err = fmt.Errorf("fail to establish google client: %+v", err)
		return
	}

	srv, err := sheets.New(client)
	spreadsheetId := "1DD70bqRm4W_Uts5do6vOO3U2C6YqY89EPuc2cfqVnW8"
	readRange := "台北市/新北市!A:Q"
	resp, err := srv.Spreadsheets.Values.Get(spreadsheetId, readRange).Do()
	if err != nil {
		log.Errorf(ctx, "unable to retrieve data from sheet. error: %+v", err)
		err = fmt.Errorf("unable to retrieve data from sheet. error: %+v", err)
		return
	}

	mapApiClient := GoogleMapApiClient{apiKey: GOOG_MAP_APIKEY}
	if len(resp.Values[1:]) > 0 {
		cafes = []Cafe{}
		for _, row := range resp.Values[2:] {
			rowLen := len(row)
			if rowLen < 16 {
				log.Warningf(ctx, "ignore the invalid cafe item: %+v", row)
				continue
			}
			cafe := Cafe{
				Name:        row[0].(string),
				Wifi:        row[1].(string),
				Space:       row[2].(string),
				Clam:        row[3].(string),
				Tasty:       row[4].(string),
				Price:       row[5].(string),
				Feeling:     row[6].(string),
				MRTFriendly: row[7].(string),
				Station:     row[9].(string),
				Address:     row[10].(string),
				TimeLimited: row[11].(string),
				Plug:        row[12].(string),
				Comments:    row[14].(string),
			}
			if rowLen == 17 {
				cafe.Link = row[16].(string)
			}

			tr := &urlfetch.Transport{Context: ctx}
			if cafe.Address != "" {
				lat, long, err := mapApiClient.getGeocoding(tr.RoundTrip, cafe.Address)
				if err == nil {
					cafe.Latitude = lat
					cafe.Longitude = long
					cafe.Geohash = geohash.EncodeWithPrecision(lat, long, 8)
				} else {
					log.Warningf(ctx, "can not get geocoding: %+v", err)
				}
			}
			cafes = append(cafes, cafe)
		}
	} else {
		err = fmt.Errorf("no data found")
	}
	return
}

func loadCafeFromDataStore(ctx context.Context) ([]Cafe, error) {
	lock.Lock()
	defer lock.Unlock()

	var err error
	cafes := []Cafe{}

	q := datastore.NewQuery("")
	_, err = q.GetAll(ctx, &cafes)
	return cafes, err
}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Hi, do you love drinking coffe?")

	var err error
	reset := r.FormValue("reset")
	ctx := appengine.NewContext(r)

	if reset == "" {
		log.Infof(ctx, "load cafe data from datastore")
		cafes, err = loadCafeFromDataStore(ctx)
		if len(cafes) != 0 {
			return
		}
	}

	cafes, err = loadCafeData(ctx)
	cafeKeys := []*datastore.Key{}

	for _, cafe := range cafes {
		cafeKeys = append(cafeKeys, datastore.NewKey(ctx, "cafes", cafe.Name, 0, nil))
	}

	_, err = datastore.PutMulti(ctx, cafeKeys, cafes)
	if err != nil {
		log.Errorf(ctx, err.Error())
	}
}

func fbSendTextMessage(ctx context.Context, senderId int64, text string, quickReplies []map[string]string) (err error) {
	var message map[string]interface{}
	if quickReplies != nil {
		message = map[string]interface{}{
			"text":          text,
			"quick_replies": quickReplies,
		}
	} else {
		message = map[string]interface{}{"text": text}
	}

	payload := map[string]interface{}{
		"recipient": FBRecipient{senderId},
		"message":   message,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return
	}

	log.Debugf(ctx, "Payload %s", b)
	req, err := http.NewRequest("POST", FBMessageURI, bytes.NewBuffer(b))
	if err != nil {
		return
	}
	req.Header.Add("Content-Type", "application/json")

	tr := &urlfetch.Transport{Context: ctx}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Infof(ctx, "Deliver status: %s", resp.Status)
		buffer := bytes.NewBuffer([]byte{})
		_, err = io.Copy(buffer, resp.Body)
		log.Infof(ctx, buffer.String())
	}
	return
}

func fbSendGeneralTemplate(ctx context.Context, senderId int64, elements json.RawMessage) (err error) {
	msgPayload := FBMessageTemplate{
		Type:     "generic",
		Elements: elements,
	}

	msgBuf, err := json.Marshal(&msgPayload)
	if err != nil {
		return
	}

	payload := map[string]interface{}{
		"recipient": FBRecipient{senderId},
		"message": map[string]interface{}{
			"attachment": &FBMessageAttachment{
				Type:    "template",
				Payload: json.RawMessage(msgBuf),
			},
		},
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return
	}

	log.Debugf(ctx, "Payload %s", b)
	req, err := http.NewRequest("POST", FBMessageURI, bytes.NewBuffer(b))
	if err != nil {
		return
	}
	req.Header.Add("Content-Type", "application/json")

	tr := &urlfetch.Transport{Context: ctx}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return
	}

	if resp.StatusCode != 200 {
		log.Infof(ctx, "Deliver status: %s", resp.Status)
		buffer := bytes.NewBuffer([]byte{})
		_, err = io.Copy(buffer, resp.Body)
		log.Infof(ctx, buffer.String())
	}
	return
}

func getShortAddr(ctx context.Context, id string, latitude, longitude float64) (shortAddr string) {
	tr := &urlfetch.Transport{Context: ctx}

	if item, err := memcache.Get(ctx, id); err == memcache.ErrCacheMiss {
		r, err := getAddress(tr.RoundTrip, latitude, longitude)
		defer time.Sleep(500 * time.Millisecond)
		if err != nil {
			log.Errorf(ctx, err.Error())
		}
		log.Infof(ctx, "Address: %+v", r)
		item := &memcache.Item{
			Key:   id,
			Value: []byte(fmt.Sprintf("%s%s,%s", r.Address.State, r.Address.Suburb, r.Address.Road)),
		}
		err = memcache.Add(ctx, item)
		if err != nil {
			log.Errorf(ctx, err.Error())
		} else {
			shortAddr = string(item.Value)
		}
	} else if err != nil {
		log.Errorf(ctx, "error getting item: %v", err)
	} else {
		shortAddr = string(item.Value)
	}
	return
}

func getDistances(lat1, long1, lat2, long2 float64) float64 {
	return math.Sqrt(math.Pow((lat2-lat1)*110, 2) + math.Pow((long2-long1)*110, 2))
}

func generateTemplateElements(ctx context.Context, items []map[string]interface{}) (elements []map[string]interface{}) {
	elements = []map[string]interface{}{}

	for _, item := range items {
		element := map[string]interface{}{
			"title":     item["title"],
			"image_url": item["image_url"],
			"item_url":  item["item_url"],
			"subtitle":  item["subtitle"],
			"buttons":   item["buttons"],
		}
		elements = append(elements, element)
	}
	return
}

func cafeToFBTemplate(cafes []Cafe) (summary, items []byte, n int) {
	results := []map[string]interface{}{}

	if len(cafes) == 0 {
		return nil, nil, 0
	}

	markers := []string{}

	for _, cafe := range cafes {
		markers = append(markers, fmt.Sprintf("%f,%f", cafe.Latitude, cafe.Longitude))

		if len(results) < 10 {
			element := map[string]interface{}{
				"title":     fmt.Sprintf("%s", cafe.Name),
				"image_url": fmt.Sprintf("https://maps.googleapis.com/maps/api/staticmap?markers=%f,%f&zoom=15&size=400x200", cafe.Latitude, cafe.Longitude),
				"item_url":  cafe.Link,
				"subtitle": fmt.Sprintf(
					"好喝: %s | Wifi: %s | 安靜: %s\n限時: %s | 插座: %s | 便宜: %s\n地址: %s",
					cafe.Tasty, cafe.Wifi, cafe.Clam,
					cafe.TimeLimited, cafe.Plug, cafe.Price,
					cafe.Address),
				"buttons": []FBButtonItem{
					FBButtonItem{
						Type:  "web_url",
						Title: "View in Maps",
						Url:   fmt.Sprintf("http://maps.apple.com/maps?q=%s&z=16", cafe.Address),
					},
					FBButtonItem{
						Type:  "web_url",
						Title: "View in Google Maps",
						Url:   fmt.Sprintf("http://maps.google.com.tw/?q=%s", cafe.Address),
					},
				},
			}
			results = append(results, element)
		}
	}

	summaryResults := []map[string]interface{}{
		map[string]interface{}{
			"title": "咖啡店分佈圖",
			"item_url": fmt.Sprintf(
				"https://maps.googleapis.com/maps/api/staticmap?zoom=15&size=400x200&markers=%s",
				strings.Join(markers, "|")),
			"image_url": fmt.Sprintf(
				"https://maps.googleapis.com/maps/api/staticmap?zoom=15&size=400x200&markers=%s",
				strings.Join(markers, "|")),
		},
	}
	s, _ := json.Marshal(summaryResults)
	b, _ := json.Marshal(results)
	return s, b, len(cafes)
}

func findCafeByGeocoding(ctx context.Context, cafes []Cafe, lat, long float64, precision int) []Cafe {
	filteredCafes := []Cafe{}

	h := geohash.EncodeWithPrecision(lat, long, precision)
	for _, cafe := range cafes {
		if strings.HasPrefix(cafe.Geohash, h) {
			filteredCafes = append(filteredCafes, cafe)
		}
	}

	return filteredCafes
}

func findCafeByLocation(ctx context.Context, location string) []Cafe {
	filteredCafes := []Cafe{}
	tr := &urlfetch.Transport{Context: ctx}
	mapApiClient := GoogleMapApiClient{apiKey: GOOG_MAP_APIKEY}
	lat, long, err := mapApiClient.getGeocoding(tr.RoundTrip, location)
	if err == nil {
		filteredCafes = findCafeByGeocoding(ctx, cafes, lat, long, 6)
	} else {
		log.Warningf(ctx, "can not get geocoding: %+v", err)
	}
	return filteredCafes
}

func sendCafeMessages(ctx context.Context, filteredCafes []Cafe, senderId int64) (returnText string) {
	summary, items, n := cafeToFBTemplate(filteredCafes)

	if n == 0 {
		returnText = "這的地點附近沒有任何咖啡店"
	} else {
		if err := fbSendGeneralTemplate(ctx, senderId, json.RawMessage(summary)); err != nil {
			returnText = "查詢失敗"
		}
		if err := fbSendGeneralTemplate(ctx, senderId, json.RawMessage(items)); err != nil {
			returnText = "查詢失敗"
		}
	}
	return
}

func fbCBPostHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	ctx := appengine.NewContext(r)

	if len(cafes) == 0 {
		cafes, _ = loadCafeFromDataStore(ctx)
	}

	var fbObject FBObject
	buf := &bytes.Buffer{}
	_, err := io.Copy(buf, r.Body)
	if err != nil {
		log.Errorf(ctx, "%s", err.Error())
		http.Error(w, "unable to parse fb object from body", http.StatusInternalServerError)
	}

	b := buf.Bytes()
	log.Infof(ctx, "%s", b)

	err = json.Unmarshal(b, &fbObject)

	if err != nil {
		log.Errorf(ctx, "%s", err.Error())
		http.Error(w, "unable to parse fb object from body", http.StatusInternalServerError)
	}

	fbMessages := fbObject.Entry[0].Messaging

	for _, fbMsg := range fbMessages {
		senderId := fbMsg.Sender.Id
		user, ok := users[senderId]
		if !ok {
			user = &User{
				Id: senderId,
			}
			users[senderId] = user
		}
		log.Debugf(ctx, "User: %+v", user)
		log.Debugf(ctx, "Message: %+v", fbMsg)

		var (
			err        error
			returnText string
		)

		if fbMsg.Content != nil {
			// Dealing with location attachments
			attachments := fbMsg.Content.Attachments
			if len(attachments) != 0 && attachments[0].Type == "location" {
				log.Debugf(ctx, "Receive attachemnt message")
				payload := FBLocationAttachment{}
				err = json.Unmarshal(attachments[0].Payload, &payload)
				if err != nil {
					log.Errorf(ctx, err.Error())
					return
				}
				lat := payload.Coordinates.Latitude
				long := payload.Coordinates.Longitude

				if user.TodoAction == "FIND_CAFE" {
					user.TodoAction = ""
					filteredCafes := findCafeByGeocoding(ctx, cafes, lat, long, 6)
					returnText = sendCafeMessages(ctx, filteredCafes, senderId)
				} else {
					text := "尋找這個地點周圍的咖啡店?"
					quickReplies := []map[string]string{
						map[string]string{
							"content_type": "text",
							"title":        "是",
							"payload":      fmt.Sprintf("FIND_CAFE_GEOCODING:%f,%f", lat, long),
						},
						map[string]string{
							"content_type": "text",
							"title":        "不是",
							"payload":      "KIDDING",
						},
					}
					err = fbSendTextMessage(ctx, senderId, text, quickReplies)
				}
			} else if fbMsg.Content.QuickReplay != nil {
				log.Debugf(ctx, "Receive QuickReply: %+v", fbMsg.Content.QuickReplay)
				payload := fbMsg.Content.QuickReplay.Payload
				payloadItems := strings.Split(payload, ":")
				if len(payloadItems) != 0 {
					switch payloadItems[0] {
					case "FIND_CAFE_GEOCODING":
						latlng := strings.Split(payloadItems[1], ",")
						if len(latlng) != 2 {
							log.Errorf(ctx, "FIND_CAFE postback arguments error: %+v", latlng)
							returnText = "查詢錯誤"
						} else {
							lat, err := strconv.ParseFloat(latlng[0], 64)
							if err != nil {
								return
							}
							long, err := strconv.ParseFloat(latlng[1], 64)
							if err != nil {
								return
							}
							filteredCafes := findCafeByGeocoding(ctx, cafes, lat, long, 6)
							returnText = sendCafeMessages(ctx, filteredCafes, senderId)
						}
					case "FIND_CAFE_LOCATION":
						if len(payloadItems) == 2 && payloadItems[1] != "" {
							filteredCafes := findCafeByLocation(ctx, fmt.Sprintf("%s台北", payloadItems[1]))
							returnText = sendCafeMessages(ctx, filteredCafes, senderId)
						}
					case "FIND_CAFE":
						text := "想去哪喝呢？"
						quickReplies := []map[string]string{
							map[string]string{
								"content_type": "location",
							},
							map[string]string{
								"content_type": "text",
								"title":        "取消",
								"payload":      "CANCEL",
							},
						}
						err = fbSendTextMessage(ctx, senderId, text, quickReplies)
						user.TodoAction = "FIND_CAFE"
					case "CANCEL":
						user.TodoAction = ""
						err = fbSendTextMessage(ctx, senderId, "好，我知道了，有需要在跟我說。", nil)
					case "KIDDING":
						err = fbSendTextMessage(ctx, senderId, "你有什麼毛病？", nil)
					}
				}
			} else {
				log.Debugf(ctx, "Receive text")
				text := fbMsg.Content.Text
				q := strings.ToLower(text)
				switch q {
				case "get started":
					fallthrough
				case "hi", "hello", "你好", "您好":
					user.TodoAction = ""
					returnText = WELCOME_TEXT
				default:
					switch user.TodoAction {
					case "FIND_CAFE":
						user.TodoAction = ""
						filteredCafes := findCafeByLocation(ctx, fmt.Sprintf("%s台北", q))
						returnText = sendCafeMessages(ctx, filteredCafes, senderId)
					default:
						user.TodoAction = ""
						tr := &urlfetch.Transport{Context: ctx}
						r, err := fetchIntent(tr.RoundTrip, q, false)
						log.Infof(ctx, "LUIS Result: %+v", r)
						if err != nil {
							returnText = "我身體不太舒服，等等再回你"
						}
						if r.TopScoringIntent.Intent == "FindCafe" {
							locations := []string{}
							for _, e := range r.Entities {
								if e.Type == "Location" {
									locations = append(locations, e.Entity)
								}
							}
							if len(locations) > 0 {
								filteredCafes := findCafeByLocation(ctx, fmt.Sprintf("%s台北", locations[0]))
								returnText = sendCafeMessages(ctx, filteredCafes, senderId)
							} else {
								user.TodoAction = "FIND_CAFE"
								text := "看不出來你想要的位置，可以幫我標記一下嗎?"
								quickReplies := []map[string]string{
									map[string]string{
										"content_type": "location",
									},
									map[string]string{
										"content_type": "text",
										"title":        "取消",
										"payload":      "CANCEL",
									},
								}
								err = fbSendTextMessage(ctx, senderId, text, quickReplies)
							}
						} else {
							locations := []string{}
							for _, e := range r.Entities {
								if e.Type == "Location" {
									locations = append(locations, e.Entity)
								}
							}
							if len(locations) > 0 {
								text := fmt.Sprintf("是要找「%s」附近的咖啡店嗎?", locations[0])
								quickReplies := []map[string]string{
									map[string]string{
										"content_type": "text",
										"title":        "是",
										"payload":      fmt.Sprintf("FIND_CAFE_LOCATION:%s", locations[0]),
									},
									map[string]string{
										"content_type": "text",
										"title":        "取消",
										"payload":      "CANCEL",
									},
								}
								err = fbSendTextMessage(ctx, senderId, text, quickReplies)
							} else {
								text := "想要在哪喝杯咖啡嗎?"
								quickReplies := []map[string]string{
									map[string]string{
										"content_type": "text",
										"title":        "是",
										"payload":      "FIND_CAFE",
									},
									map[string]string{
										"content_type": "text",
										"title":        "取消",
										"payload":      "CANCEL",
									},
								}
								err = fbSendTextMessage(ctx, senderId, text, quickReplies)
							}
						}
					}
				}
				if err != nil {
					log.Errorf(ctx, "%s", err.Error())
					http.Error(w, "fail to deliver a message to a client", http.StatusInternalServerError)
				}
				user.LastText = text
			}
		} else if fbMsg.Delivery != nil {
		} else if fbMsg.Postback != nil {
			log.Debugf(ctx, "Receive Postback: %+v", fbMsg.Postback)
			payload := fbMsg.Postback.Payload
			payloadItems := strings.Split(payload, ":")
			if len(payloadItems) != 0 {
				action := payloadItems[0]
				switch action {
				case "FIND_CAFE":
					text := "想找咖啡店嗎？給我一個你想搜尋的位置"
					quickReplies := []map[string]string{
						map[string]string{
							"content_type": "location",
						},
						map[string]string{
							"content_type": "text",
							"title":        "取消",
							"payload":      "CANCEL",
						},
					}
					err = fbSendTextMessage(ctx, senderId, text, quickReplies)
					user.TodoAction = "FIND_CAFE"
				case "GET_STARTED":
					err = fbSendTextMessage(ctx, senderId, WELCOME_TEXT, nil)
					fallthrough
				default:
					user.TodoAction = ""
				}
			}
		}
		if returnText != "" {
			err = fbSendTextMessage(ctx, senderId, returnText, nil)
		}
	}
	fmt.Fprint(w, "")
}

func fbCBHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if r.FormValue("hub.verify_token") == BOT_TOKEN {
			challenge := r.FormValue("hub.challenge")
			fmt.Fprint(w, challenge)
		} else {
			http.Error(w, "Invalid Token", http.StatusForbidden)
		}
	} else if r.Method == "POST" {
		fbCBPostHandler(w, r)
	} else {
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}
