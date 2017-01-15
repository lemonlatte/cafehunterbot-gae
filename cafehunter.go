package cafehunter

import (
	"bytes"
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
	"googlemaps.github.io/maps"

	"github.com/TomiHiltunen/geohash-golang"
	"github.com/lemonlatte/ambassador"
	"github.com/looplab/fsm"
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
	State      string
	FSM        *fsm.FSM
	TodoAction string
	LastText   string
}

var users map[string]*User = map[string]*User{}

type Place struct {
	Name             string
	FormattedAddress string
	Geometry         maps.AddressGeometry
	PlaceID          string
}

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
						Title: "View in Cafenomad",
						Url:   fmt.Sprintf("https://cafenomad.tw/shop/%s", cafe.Id),
					},
					ambassador.FBButtonItem{
						Type:  "web_url",
						Title: "View in Google Maps",
						Url:   fmt.Sprintf("https://maps.google.com/?q=%s", cafe.Address),
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
	areas = append(areas, h)
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

func resolveGeocoding(ctx context.Context, location string) (places []Place, err error) {
	client := urlfetch.Client(ctx)
	c, err := maps.NewClient(maps.WithAPIKey(GOOG_MAP_APIKEY), maps.WithHTTPClient(client))
	if err != nil {
		log.Errorf(ctx, "can not create google map api client: %s", err)
		return
	}

	places = make([]Place, 0)
	placesResp, err := c.TextSearch(ctx, &maps.TextSearchRequest{
		Query:    fmt.Sprintf("%s+in+Taiwan", location),
		Language: "zh-TW",
	})
	if err == nil && len(placesResp.Results) > 0 {
		for _, r := range placesResp.Results {
			p := Place{
				Name:             r.Name,
				Geometry:         r.Geometry,
				FormattedAddress: r.FormattedAddress,
				PlaceID:          r.PlaceID,
			}
			places = append(places, p)
		}
	} else {
		geocodingResults, err := c.Geocode(ctx, &maps.GeocodingRequest{
			Address: location,
			Components: map[maps.Component]string{
				maps.ComponentCountry: "TW",
			},
			Language: "zh-TW",
		})
		if err == nil && len(geocodingResults) > 0 {
			for _, r := range geocodingResults {
				p := Place{
					Name:             r.FormattedAddress,
					Geometry:         r.Geometry,
					FormattedAddress: r.FormattedAddress,
					PlaceID:          r.PlaceID,
				}
				places = append(places, p)
			}
		} else {
			log.Warningf(ctx, "no results found")
		}
	}

	if len(places) > 8 {
		places = places[0:8]
	}
	return
}

func findCafeByLocation(ctx context.Context, location string) (cafes []Cafe, err error) {
	client := urlfetch.Client(ctx)
	c, err := maps.NewClient(maps.WithAPIKey(GOOG_MAP_APIKEY), maps.WithHTTPClient(client))
	if err != nil {
		log.Errorf(ctx, "can not get geocoding: %s", err)
		return
	}

	results, err := c.Geocode(ctx, &maps.GeocodingRequest{Address: location})
	if err != nil {
		log.Errorf(ctx, "can not get geocoding: %s", err)
		return
	}

	if len(results) == 0 {
		log.Warningf(ctx, "no location found")
		return
	}

	lat := results[0].Geometry.Location.Lat
	long := results[0].Geometry.Location.Lng

	return findCafeByGeocoding(ctx, lat, long, 7), nil
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

func askLocationConfirm(a ambassador.Ambassador, places []Place, senderId string) (err error) {
	locationChoiceReplies := []map[string]string{}
	for _, p := range places {
		lat := p.Geometry.Location.Lat
		long := p.Geometry.Location.Lng
		locationChoiceReplies = append(locationChoiceReplies, map[string]string{
			"content_type": "text",
			"title":        p.Name,
			"payload":      fmt.Sprintf("FIND_CAFE_GEOCODING:%f,%f", lat, long),
		})

	}
	locationChoiceReplies = append(locationChoiceReplies, map[string]string{
		"content_type": "text",
		"title":        "都不是",
		"payload":      "CANCEL",
	}, map[string]string{
		"content_type": "location",
	})
	text := "範圍不夠清楚，幫我從下方選出最接近的位置"
	err = a.AskQuestion(senderId, text, locationChoiceReplies)
	return
}

func confirmLocation(ctx context.Context, locations []string, user *User, a ambassador.Ambassador) (err error) {
	if len(locations) == 0 {
		return fmt.Errorf("logic error: this case should not happen")
	}

	if len(locations) == 1 {
		location := locations[0]
		err = a.SendText(user.Id, fmt.Sprintf("為您尋找「%s」的咖啡店", location))
		var places []Place
		places, err = resolveGeocoding(ctx, location)

		if len(places) > 1 {
			user.FSM.Event("getConfusedLocation")
			err = askLocationConfirm(a, places, user.Id)
		} else {
			user.FSM.Event("responeResult")
			if len(places) == 0 {
				err = a.SendText(user.Id, "很抱歉，無法在我的地圖上找到這個地點")
			} else if len(places) == 1 {
				var filteredCafes []Cafe
				lat := places[0].Geometry.Location.Lat
				long := places[0].Geometry.Location.Lng
				filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
				err = sendCafeMessages(a, filteredCafes, user.Id)
			}
		}
	} else {
		text := "你提到了一個以上的位置，請問哪個是你要的?"
		locationReplies := []map[string]string{}
		for _, l := range locations {
			locationReplies = append(locationReplies, map[string]string{
				"content_type": "text",
				"title":        l,
				"payload":      fmt.Sprintf("FIND_CAFE_LOCATION:%s", l),
			})
		}
		locationReplies = append(locationReplies, map[string]string{
			"content_type": "text",
			"title":        "都不是",
			"payload":      "CANCEL",
		}, map[string]string{
			"content_type": "location",
		})
		err = a.AskQuestion(user.Id, text, locationReplies)
	}
	return
}

func contextAnalysis(ctx context.Context, user *User, message string, a ambassador.Ambassador) (err error) {
	tr := &urlfetch.Transport{Context: ctx}
	r, err := fetchIntent(tr.RoundTrip, message, false)
	log.Infof(ctx, "LUIS Result: %+v", r)
	if err != nil {
		err = a.SendText(user.Id, "機器人的識別功能發生故障")
	} else {
		locations := []string{}
		for _, e := range r.Entities {
			if e.Type == "Location" {
				locations = append(locations, e.Entity)
			}
		}

		if r.TopScoringIntent.Intent == "FindCafe" {
			user.FSM.Event("receiveIntent")
			if len(locations) > 0 {
				err = confirmLocation(ctx, locations, user, a)
			} else {
				// user.FSM.Event("unknownLocation")
				text := "找哪裡的咖啡？給我一個地名或是幫我標記出來？"
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
				err = a.AskQuestion(user.Id, text, quickReplies)
			}
		} else {
			if len(locations) == 0 {
				// 沒意圖，沒地址直接取消
				user.FSM.Event("cancel")
				text := "啥？我只負責找咖啡店喔。" // 這邊需要廢文產生器
				err = a.SendText(user.Id, text)
			} else {
				// user.FSM.Event("unknownIntent")
				// 有地址，暫時假設要找咖啡店
				user.FSM.Event("receiveIntent")
				err = confirmLocation(ctx, locations, user, a)
			}
		}
	}
	return
}

func newUser(senderId string) *User {
	user := &User{
		Id:    senderId,
		State: "STANDBY",
	}
	user.FSM = fsm.NewFSM("STANDBY", fsm.Events{
		{Name: "greeting", Src: []string{"STANDBY"}, Dst: "STANDBY"},
		{Name: "receiveGeocoding", Src: []string{"STANDBY", "UNSURE_LOCATION"}, Dst: "LOCATION_CONFIRMED"},
		{Name: "receiveAddress", Src: []string{"STANDBY", "UNSURE_LOCATION"}, Dst: "LOCATION_CONFIRMED"},
		{Name: "unknownIntent", Src: []string{"STANDBY"}, Dst: "STANDBY"},
		{Name: "receiveIntent", Src: []string{"STANDBY"}, Dst: "INTENT_CONFIRMED"},

		// {Name: "unknownLocation", Src: []string{"INTENT_CONFIRMED"}, Dst: "UNKNOWN_LOCATION"},
		{Name: "getConfusedLocation", Src: []string{"STANDBY", "INTENT_CONFIRMED"}, Dst: "UNSURE_LOCATION"},
		{Name: "responeResult", Src: []string{"INTENT_CONFIRMED", "UNSURE_LOCATION", "LOCATION_CONFIRMED"}, Dst: "STANDBY"},

		{Name: "cancel", Src: []string{"INTENT_CONFIRMED", "UNKNOWN_LOCATION", "UNSURE_LOCATION"}, Dst: "STANDBY"},
	}, fsm.Callbacks{
		"after_event": func(event *fsm.Event) {
			user.State = event.Dst
		},
	})
	return user
}

func commandHandler(ctx context.Context, user *User, payload string, a ambassador.Ambassador) (err error) {
	if payloadItems := strings.Split(payload, ":"); len(payloadItems) != 0 {
		switch payloadItems[0] {
		case "FIND_CAFE_GEOCODING":
			user.FSM.Event("responeResult")
			latlng := strings.Split(payloadItems[1], ",")
			if len(latlng) != 2 {
				log.Errorf(ctx, "FIND_CAFE postback arguments error: %+v", latlng)
				err = a.SendText(user.Id, "查詢錯誤")
			} else {
				lat, err := strconv.ParseFloat(latlng[0], 64)
				if err != nil {
					return err
				}
				long, err := strconv.ParseFloat(latlng[1], 64)
				if err != nil {
					return err
				}
				filteredCafes := findCafeByGeocoding(ctx, lat, long, 7)
				err = sendCafeMessages(a, filteredCafes, user.Id)
			}
		case "FIND_CAFE_LOCATION":
			if len(payloadItems) == 2 && payloadItems[1] != "" {
				var places []Place
				places, err = resolveGeocoding(ctx, payloadItems[1])
				if err != nil || len(places) == 0 {
					user.FSM.Event("responeResult")
					err = a.SendText(user.Id, "無法辨識的地點")
				} else if len(places) == 1 {
					user.FSM.Event("responeResult")
					var filteredCafes []Cafe
					lat := places[0].Geometry.Location.Lat
					long := places[0].Geometry.Location.Lng
					filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
					err = sendCafeMessages(a, filteredCafes, user.Id)
				} else {
					user.FSM.Event("getConfusedLocation")
					err = askLocationConfirm(a, places, user.Id)
				}
			}
		case "FIND_CAFE":
			user.FSM.Event("receiveIntent")
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
			err = a.AskQuestion(user.Id, text, answers)
		case "CANCEL":
			user.FSM.Event("cancel")
			err = a.SendText(user.Id, "好，我知道了，有需要再跟我說。")
		case "KIDDING":
			user.FSM.Event("cancel")
			err = a.SendText(user.Id, "不喝就不喝。")
		case "GET_STARTED":
			user.FSM.Event("greeting")
			err = a.SendText(user.Id, WELCOME_TEXT)
		}
	} else {
		return fmt.Errorf("invalid command string")
	}
	return
}

func standbyHandler(ctx context.Context, user *User, msg ambassador.Message, a ambassador.Ambassador) (err error) {
	switch msgContent := msg.Content.(type) {
	case *ambassador.TextContent:
		q := strings.ToLower(msgContent.Text)
		switch q {
		case "get started", "hi", "hello", "安安", "你好", "妳好", "您好":
			user.FSM.Event("greeting")
			err = a.SendText(user.Id, WELCOME_TEXT)
			if err != nil {
				log.Errorf(ctx, err.Error())
			}
		default:
			err = contextAnalysis(ctx, user, q, a)
		}
	case *ambassador.CommandContent:
		err = commandHandler(ctx, user, msgContent.Payload, a)
	case *ambassador.LocationContent:
		text := "尋找這個地點周圍的咖啡店?"
		quickReplies := []map[string]string{
			map[string]string{
				"content_type": "text",
				"title":        "是",
				"payload":      fmt.Sprintf("FIND_CAFE_GEOCODING:%f,%f", msgContent.Lat, msgContent.Lon),
			},
			map[string]string{
				"content_type": "text",
				"title":        "不是",
				"payload":      "KIDDING",
			},
		}
		err = a.AskQuestion(user.Id, text, quickReplies)
	default:
	}
	return
}

func intentConfirmHandler(ctx context.Context, user *User, msg ambassador.Message, a ambassador.Ambassador) (err error) {
	switch msgContent := msg.Content.(type) {
	case *ambassador.TextContent:
		q := strings.ToLower(msgContent.Text)
		var places []Place
		places, err = resolveGeocoding(ctx, q)
		if len(places) == 0 {
			user.FSM.Event("responeResult")
			err = a.SendText(user.Id, "無法辨識的地點")
		} else if len(places) == 1 {
			var filteredCafes []Cafe
			user.FSM.Event("responeResult")
			lat := places[0].Geometry.Location.Lat
			long := places[0].Geometry.Location.Lng
			filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
			err = sendCafeMessages(a, filteredCafes, user.Id)
		} else {
			user.FSM.Event("getConfusedLocation")
			err = askLocationConfirm(a, places, user.Id)
		}
	case *ambassador.CommandContent:
		err = commandHandler(ctx, user, msgContent.Payload, a)
	case *ambassador.LocationContent:
		user.FSM.Event("responeResult")
		filteredCafes := findCafeByGeocoding(ctx, msgContent.Lat, msgContent.Lon, 7)
		err = sendCafeMessages(a, filteredCafes, user.Id)
	}
	return
}

func unsureLocationHandler(ctx context.Context, user *User, msg ambassador.Message, a ambassador.Ambassador) (err error) {
	switch msgContent := msg.Content.(type) {
	case *ambassador.TextContent:
		q := strings.ToLower(msgContent.Text)
		var places []Place
		places, err = resolveGeocoding(ctx, q)
		if len(places) == 0 {
			user.FSM.Event("responeResult")
			err = a.SendText(user.Id, "無法辨識的地點")
		} else if len(places) == 1 {
			var filteredCafes []Cafe
			user.FSM.Event("responeResult")
			lat := places[0].Geometry.Location.Lat
			long := places[0].Geometry.Location.Lng
			filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
			err = sendCafeMessages(a, filteredCafes, user.Id)
		} else {
			user.FSM.Event("getConfusedLocation")
			err = askLocationConfirm(a, places, user.Id)
		}
	case *ambassador.CommandContent:
		err = commandHandler(ctx, user, msgContent.Payload, a)
	case *ambassador.LocationContent:
		user.FSM.Event("receiveGeocoding")
		text := "尋找這個地點周圍的咖啡店?"
		quickReplies := []map[string]string{
			map[string]string{
				"content_type": "text",
				"title":        "是",
				"payload":      fmt.Sprintf("FIND_CAFE_GEOCODING:%f,%f", msgContent.Lat, msgContent.Lon),
			},
			map[string]string{
				"content_type": "text",
				"title":        "不是",
				"payload":      "KIDDING",
			},
		}
		err = a.AskQuestion(user.Id, text, quickReplies)
	default:
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

	log.Infof(ctx, "Incoming message: %s", buf.String())

	messages, err := a.Translate(buf)
	if err != nil {
		log.Errorf(ctx, "%s", err.Error())
		http.Error(w, "unable to parse fb object from body", http.StatusInternalServerError)
	}

	for _, msg := range messages {
		senderId := msg.SenderId

		user, ok := users[senderId]
		if !ok {
			user = newUser(senderId)
			users[senderId] = user
		}
		log.Debugf(ctx, "User %s is at state: %s", user.Id, user.State)

		var err error

		switch user.State {
		case "STANDBY":
			err = standbyHandler(ctx, user, msg, a)
		case "INTENT_CONFIRMED":
			err = intentConfirmHandler(ctx, user, msg, a)
		case "UNSURE_LOCATION":
			err = unsureLocationHandler(ctx, user, msg, a)
		default:
		}

		if err != nil {
			log.Errorf(ctx, "an error occurs on message delivery: %s", err.Error())
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
