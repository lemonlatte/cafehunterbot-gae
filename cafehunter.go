package cafehunter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"

	"github.com/TomiHiltunen/geohash-golang"
	"github.com/lemonlatte/ambassador"
	// "github.com/looplab/fsm"
	"gopkg.in/zabawaba99/firego.v1"
)

const (
	BOT_TOKEN       = ""
	PAGE_TOKEN      = ""
	GOOG_MAP_APIKEY = ""
	FIREBASE_AUTH_TOKEN = ""

	FBMessageURI = "https://graph.facebook.com/v2.6/me/messages?access_token=" + PAGE_TOKEN
	WELCOME_TEXT = `你好，歡迎使用 Café Hunter。請用簡單的句子跟我對話，例如：「我要找咖啡店」、「我想喝咖啡」、「士林有什麼推薦的咖啡店嗎？」`
)

var lock sync.Mutex = sync.Mutex{}

type Location struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"long"`
}

type User struct {
	Id         string
	TodoAction string
	LastText   string
}

var users map[string]*User = map[string]*User{}

func init() {
	http.HandleFunc("/fbCallback", fbCBHandler)
	http.HandleFunc("/", handler)
}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Hi, do you love drinking coffe?")
}

func pointToStar(point float64) (starString string) {
	digits := int64(point)
	floating := point - float64(digits)

	for i := int64(0); i < digits; i++ {
		starString += "🌟"
	}

	if floating > 0 {
		starString += "½"
	}
	return
}

func cafeToFBTemplate(cafes []Cafe) (summary, items interface{}, n int) {
	resultItems := []map[string]interface{}{}

	if len(cafes) == 0 {
		return nil, nil, 0
	}

	markers := []string{}

	for _, cafe := range cafes {
		markers = append(markers, fmt.Sprintf("%f,%f", cafe.Latitude, cafe.Longitude))

		if len(resultItems) < 10 {
			element := map[string]interface{}{
				"title":     fmt.Sprintf("%s", cafe.Name),
				"image_url": fmt.Sprintf("https://maps.googleapis.com/maps/api/staticmap?markers=%f,%f&zoom=15&size=400x200", cafe.Latitude, cafe.Longitude),
				"item_url":  cafe.Link,
				"subtitle": fmt.Sprintf(
					"好喝: %s | Wifi: %s \n安靜: %s | 便宜: %s\n地址: %s",
					pointToStar(cafe.Tasty), pointToStar(cafe.Wifi),
					pointToStar(cafe.Quiet), pointToStar(cafe.Price),
					cafe.Address),
				"buttons": []ambassador.FBButtonItem{
					// ambassador.FBButtonItem{
					// 	Type:  "web_url",
					// 	Title: "View in Maps",
					// 	Url:   fmt.Sprintf("http://maps.apple.com/maps?q=%s&z=16", cafe.Address),
					// },
					ambassador.FBButtonItem{
						Type:  "web_url",
						Title: "View in Google Maps",
						Url:   fmt.Sprintf("http://maps.google.com.tw/?q=%s", cafe.Address),
					},
				},
			}
			resultItems = append(resultItems, element)
		}
	}

	summaryItems := []map[string]interface{}{
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

	return summaryItems, resultItems, len(cafes)
}

func findCafeByGeocoding(ctx context.Context, lat, long float64, precision int) []Cafe {
	filteredCafes := []Cafe{}

	h := geohash.EncodeWithPrecision(lat, long, precision)
	areas := geohash.CalculateAllAdjacent(h)

	client := urlfetch.Client(ctx)

	firegoClient := firego.New("https://cafe-hunter.firebaseio.com", client)
	firegoClient.Auth(FIREBASE_AUTH_TOKEN)

	for _, a := range areas {
		v := map[string]Cafe{}
		err := firegoClient.Child("cafes").OrderBy("geohash").StartAt(a).EndAt(a + "~").Value(&v)
		if err != nil {
			log.Errorf(ctx, "can not fetch cafes: %s", err.Error())
		}
		for _, cafe := range v {
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
		filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
	} else {
		log.Warningf(ctx, "can not get geocoding: %+v", err)
	}
	return filteredCafes
}

func sendCafeMessages(a ambassador.Ambassador, filteredCafes []Cafe, senderId string) (err error) {
	summary, items, n := cafeToFBTemplate(filteredCafes)

	if n == 0 {
		err = a.SendText(senderId, "無法在我的記憶裡找到那附近的咖啡店。")
	} else {
		if err = a.SendTemplate(senderId, summary); err != nil {
			return
		}
		err = a.SendTemplate(senderId, items)
	}
	return
}

func fbCBPostHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	ctx := appengine.NewContext(r)
	client := urlfetch.Client(ctx)
	a := ambassador.NewFBAmbassador(PAGE_TOKEN, client)

	buf := &bytes.Buffer{}
	_, _ = io.Copy(buf, r.Body)

	// log.Infof(ctx, buf.String())

	messages, err := a.Translate(buf)
	if err != nil {
		log.Errorf(ctx, "%s", err.Error())
		http.Error(w, "unable to parse fb object from body", http.StatusInternalServerError)
	}

	for _, msg := range messages {
		senderId := msg.SenderId

		user, ok := users[senderId]
		if !ok {
			user = &User{
				Id: senderId,
			}
			users[senderId] = user
		}

		log.Debugf(ctx, "User: %+v", user)

		var err error

		switch msgBody := msg.Body.(type) {
		case *ambassador.FBMessageContent:
			log.Debugf(ctx, "Receive content message: %+v", msgBody)
			if msgBody.IsEcho {
				log.Infof(ctx, "ignore echo message")
				break
			}
			// Dealing with location attachments
			attachments := msgBody.Attachments
			if len(attachments) != 0 && attachments[0].Type == "location" {
				log.Debugf(ctx, "Receive attachemnt message")
				payload := ambassador.FBLocationAttachment{}
				err = json.Unmarshal(attachments[0].Payload, &payload)
				if err != nil {
					log.Errorf(ctx, err.Error())
					return
				}
				lat := payload.Coordinates.Latitude
				long := payload.Coordinates.Longitude

				if user.TodoAction == "FIND_CAFE" {
					user.TodoAction = ""
					filteredCafes := findCafeByGeocoding(ctx, lat, long, 7)
					err = sendCafeMessages(a, filteredCafes, senderId)
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
					err = a.AskQuestion(senderId, text, quickReplies)
				}
			} else if msgBody.QuickReplay != nil {
				log.Debugf(ctx, "Receive QuickReply: %+v", msgBody.QuickReplay)
				payload := msgBody.QuickReplay.Payload
				payloadItems := strings.Split(payload, ":")
				if len(payloadItems) != 0 {
					switch payloadItems[0] {
					case "FIND_CAFE_GEOCODING":
						latlng := strings.Split(payloadItems[1], ",")
						if len(latlng) != 2 {
							log.Errorf(ctx, "FIND_CAFE postback arguments error: %+v", latlng)
							err = a.SendText(senderId, "查詢錯誤")
						} else {
							lat, err := strconv.ParseFloat(latlng[0], 64)
							if err != nil {
								return
							}
							long, err := strconv.ParseFloat(latlng[1], 64)
							if err != nil {
								return
							}
							filteredCafes := findCafeByGeocoding(ctx, lat, long, 7)
							err = sendCafeMessages(a, filteredCafes, senderId)
						}
					case "FIND_CAFE_LOCATION":
						if len(payloadItems) == 2 && payloadItems[1] != "" {
							filteredCafes := findCafeByLocation(ctx, fmt.Sprintf("%s台北", payloadItems[1]))
							err = sendCafeMessages(a, filteredCafes, senderId)
						}
					case "FIND_CAFE":
						text := "想去哪喝呢？"
						answers := []map[string]string{
							map[string]string{
								"content_type": "location",
							},
							map[string]string{
								"content_type": "text",
								"title":        "取消",
								"payload":      "CANCEL",
							},
						}
						err = a.AskQuestion(senderId, text, answers)
						user.TodoAction = "FIND_CAFE"
					case "CANCEL":
						user.TodoAction = ""
						err = a.SendText(senderId, "好，我知道了，有需要再跟我說。")
					case "KIDDING":
						err = a.SendText(senderId, "不喝就不喝。")
					}
				}
			} else {
				log.Debugf(ctx, "Receive text")
				text := msgBody.Text
				q := strings.ToLower(text)
				switch q {
				case "get started":
					fallthrough
				case "hi", "hello", "你好", "您好":
					user.TodoAction = ""
					err = a.SendText(senderId, WELCOME_TEXT)
				default:
					switch user.TodoAction {
					case "FIND_CAFE":
						user.TodoAction = ""
						filteredCafes := findCafeByLocation(ctx, fmt.Sprintf("%s台北", q))
						err = sendCafeMessages(a, filteredCafes, senderId)
					default:
						user.TodoAction = ""
						tr := &urlfetch.Transport{Context: ctx}
						r, err := fetchIntent(tr.RoundTrip, q, false)
						log.Infof(ctx, "LUIS Result: %+v", r)
						if err != nil {
							err = a.SendText(senderId, "機器人的識別功能發生故障")
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
								err = sendCafeMessages(a, filteredCafes, senderId)
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
								err = a.AskQuestion(senderId, text, quickReplies)
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
								err = a.AskQuestion(senderId, text, quickReplies)
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
								err = a.AskQuestion(senderId, text, quickReplies)
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
		case *ambassador.FBMessagePostback:
			log.Debugf(ctx, "Receive postback message: %+v", msgBody)
			payload := msgBody.Payload
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
					err = a.AskQuestion(senderId, text, quickReplies)
					user.TodoAction = "FIND_CAFE"
				case "GET_STARTED":
					err = a.SendText(senderId, WELCOME_TEXT)
					fallthrough
				default:
					user.TodoAction = ""
				}
			}
		case *ambassador.FBMessageDelivery, *ambassador.FBMessageRead:
			log.Debugf(ctx, "Ignored message: %+v", msgBody)
		default:
			log.Debugf(ctx, "Receive unhandled message: %+v", msgBody)
		}
		if err != nil {
			log.Errorf(ctx, "fail to deliver messages: %s", err.Error())
			// a.SendText(senderId, "我好像壞掉了")
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
