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

func newUser(senderId string) *User {
	user := &User{
		Id:    senderId,
		State: "STANDBY",
	}
	user.FSM = fsm.NewFSM("STANDBY", fsm.Events{
		{Name: "greeting", Src: []string{"STANDBY"}, Dst: "STANDBY"},
		{Name: "unknownIntent", Src: []string{"STANDBY", "UNKNOWN_INTENT"}, Dst: "UNKNOWN_INTENT"},
		{Name: "receiveIntent", Src: []string{"STANDBY", "UNKNOWN_INTENT"}, Dst: "INTENT_CONFIRMED"},
		{Name: "unknownLocation", Src: []string{"INTENT_CONFIRMED"}, Dst: "UNKNOWN_LOCATION"},
		{Name: "getConfusedLocation", Src: []string{"INTENT_CONFIRMED", "UNKNOWN_LOCATION"}, Dst: "UNSURE_LOCATION"},
		{Name: "receiveGeocoding", Src: []string{"STANDBY", "UNSURE_LOCATION"}, Dst: "LOCATION_CONFIRMED"},
		{Name: "receiveAddress", Src: []string{"STANDBY", "UNSURE_LOCATION"}, Dst: "LOCATION_CONFIRMED"},

		{Name: "responeResult", Src: []string{"UNKNOWN_INTENT", "UNKNOWN_LOCATION", "INTENT_CONFIRMED", "UNSURE_LOCATION", "LOCATION_CONFIRMED"}, Dst: "STANDBY"},

		{Name: "cancel", Src: []string{"INTENT_CONFIRMED", "UNKNOWN_INTENT", "UNKNOWN_LOCATION", "UNSURE_LOCATION"}, Dst: "STANDBY"},
	}, fsm.Callbacks{
		"after_event": func(event *fsm.Event) {
			user.State = event.Dst
		},
	})
	return user
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
			user = newUser(senderId)
			users[senderId] = user
		}
		log.Debugf(ctx, "User %s is at state: %s", user.Id, user.State)

		var fsmErr, err error

		switch msgBody := msg.Body.(type) {
		case *ambassador.FBMessageContent:
			log.Debugf(ctx, "Receive content message: %+v", msgBody)
			if msgBody.IsEcho {
				log.Debugf(ctx, "ignore echo message")
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

				switch user.State {
				case "UNKNOWN_INTENT", "UNKNOWN_LOCATION", "INTENT_CONFIRMED":
					fsmErr = user.FSM.Event("responeResult")
					filteredCafes := findCafeByGeocoding(ctx, lat, long, 7)
					err = sendCafeMessages(a, filteredCafes, senderId)
					if err != nil {
						log.Errorf(ctx, err.Error())
					}
				case "STANDBY", "UNSURE_LOCATION":
					fsmErr = user.FSM.Event("receiveGeocoding")
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
						fsmErr = user.FSM.Event("responeResult")
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
							var places []Place
							places, err = resolveGeocoding(ctx, payloadItems[1])
							if len(places) == 0 {
								fsmErr = user.FSM.Event("responeResult")
								err = a.SendText(senderId, "無法辨識的地點")
							} else if len(places) == 1 {
								var filteredCafes []Cafe
								fsmErr = user.FSM.Event("responeResult")
								lat := places[0].Geometry.Location.Lat
								long := places[0].Geometry.Location.Lng
								filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
								err = sendCafeMessages(a, filteredCafes, senderId)
							} else {
								fsmErr = user.FSM.Event("getConfusedLocation")
								err = askLocationConfirm(a, places, senderId)
							}
						}
					case "FIND_CAFE":
						fsmErr = user.FSM.Event("receiveIntent")
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
					case "CANCEL":
						fsmErr = user.FSM.Event("cancel")
						err = a.SendText(senderId, "好，我知道了，有需要再跟我說。")
					case "KIDDING":
						fsmErr = user.FSM.Event("cancel")
						err = a.SendText(senderId, "不喝就不喝。")
					}
				}
			} else {
				text := msgBody.Text
				q := strings.ToLower(text)
				switch q {
				case "get started", "hi", "hello", "你好", "您好":
					fsmErr = user.FSM.Event("greeting")
					err = a.SendText(senderId, WELCOME_TEXT)
					if err != nil {
						log.Errorf(ctx, err.Error())
					}
				default:
					switch user.State {
					case "INTENT_CONFIRMED", "UNSURE_LOCATION":
						var places []Place
						places, err = resolveGeocoding(ctx, q)
						if len(places) == 0 {
							fsmErr = user.FSM.Event("responeResult")
							err = a.SendText(senderId, "無法辨識的地點")
						} else if len(places) == 1 {
							var filteredCafes []Cafe
							fsmErr = user.FSM.Event("responeResult")
							lat := places[0].Geometry.Location.Lat
							long := places[0].Geometry.Location.Lng
							filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
							err = sendCafeMessages(a, filteredCafes, senderId)
						} else {
							fsmErr = user.FSM.Event("getConfusedLocation")
							err = askLocationConfirm(a, places, senderId)
						}
					case "UNKNOWN_LOCATION":
						tr := &urlfetch.Transport{Context: ctx}
						r, err := fetchIntent(tr.RoundTrip, q, false)
						log.Infof(ctx, "LUIS Result: %+v", r)
						if err != nil {
							err = a.SendText(senderId, "機器人的識別功能發生故障")
						} else {
							locations := []string{}
							for _, e := range r.Entities {
								if e.Type == "Location" {
									locations = append(locations, e.Entity)
								}
							}

							if len(locations) > 0 {
								var places []Place
								places, err = resolveGeocoding(ctx, locations[0])
								if len(places) == 0 {
									fsmErr = user.FSM.Event("responeResult")
									err = a.SendText(senderId, "無法辨識的地點")
								} else if len(places) == 1 {
									var filteredCafes []Cafe
									fsmErr = user.FSM.Event("responeResult")
									lat := places[0].Geometry.Location.Lat
									long := places[0].Geometry.Location.Lng
									filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
									err = sendCafeMessages(a, filteredCafes, senderId)
								} else {
									fsmErr = user.FSM.Event("getConfusedLocation")
									err = askLocationConfirm(a, places, senderId)
								}
							} else {
								fsmErr = user.FSM.Event("unknownLocation")
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
								err = a.AskQuestion(senderId, text, quickReplies)
							}
						}
					case "STANDBY", "UNKNOWN_INTENT":
						tr := &urlfetch.Transport{Context: ctx}
						r, err := fetchIntent(tr.RoundTrip, q, false)
						log.Infof(ctx, "LUIS Result: %+v", r)
						if err != nil {
							err = a.SendText(senderId, "機器人的識別功能發生故障")
						} else {
							if r.TopScoringIntent.Intent == "FindCafe" {
								fsmErr = user.FSM.Event("receiveIntent")
								locations := []string{}
								for _, e := range r.Entities {
									if e.Type == "Location" {
										locations = append(locations, e.Entity)
									}
								}

								if len(locations) > 0 {
									var places []Place
									places, err = resolveGeocoding(ctx, locations[0])
									if len(places) == 0 {
										fsmErr = user.FSM.Event("responeResult")
										err = a.SendText(senderId, "無法辨識的地點")
									} else if len(places) == 1 {
										var filteredCafes []Cafe
										fsmErr = user.FSM.Event("responeResult")
										lat := places[0].Geometry.Location.Lat
										long := places[0].Geometry.Location.Lng
										filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
										err = sendCafeMessages(a, filteredCafes, senderId)
									} else {
										fsmErr = user.FSM.Event("getConfusedLocation")
										err = askLocationConfirm(a, places, senderId)
									}
								} else {
									fsmErr = user.FSM.Event("unknownLocation")
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
									err = a.AskQuestion(senderId, text, quickReplies)
								}
							} else {
								// 直接假設要找咖啡店
								fsmErr = user.FSM.Event("receiveIntent")

								locationReplies := []map[string]string{}
								for _, e := range r.Entities {
									if e.Type == "Location" {
										locationReplies = append(locationReplies, map[string]string{
											"content_type": "text",
											"title":        e.Entity,
											"payload":      fmt.Sprintf("FIND_CAFE_LOCATION:%s", e.Entity),
										})
									}
								}

								if len(locationReplies) == 1 {
									location := locationReplies[0]["title"]
									err = a.SendText(senderId, fmt.Sprintf("為您尋找「%s」的咖啡店", location))
									var places []Place
									places, err = resolveGeocoding(ctx, location)
									if len(places) == 0 {
										fsmErr = user.FSM.Event("responeResult")
										err = a.SendText(senderId, "我看不懂這個地點，需要更清楚的描述。")
									} else if len(places) == 1 {
										var filteredCafes []Cafe
										fsmErr = user.FSM.Event("responeResult")
										lat := places[0].Geometry.Location.Lat
										long := places[0].Geometry.Location.Lng
										filteredCafes = findCafeByGeocoding(ctx, lat, long, 7)
										err = sendCafeMessages(a, filteredCafes, senderId)
									} else {
										fsmErr = user.FSM.Event("getConfusedLocation")
										err = askLocationConfirm(a, places, senderId)
									}
								} else if len(locationReplies) > 1 {
									text := "你提到了一個以上的位置，是哪個?"
									locationReplies = append(locationReplies, map[string]string{
										"content_type": "text",
										"title":        "都不是",
										"payload":      "CANCEL",
									}, map[string]string{
										"content_type": "location",
									})
									err = a.AskQuestion(senderId, text, locationReplies)
								} else {
									fsmErr = user.FSM.Event("cancel")
									text := "哈哈，我不太懂你為什麼這麼說。" // 這邊需要廢文產生器
									err = a.SendText(senderId, text)
								}
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
					fsmErr = user.FSM.Event("receiveIntent")
					text := "好喔，想要找哪裡的咖啡店？"
					err = a.AskQuestion(senderId, text, []map[string]string{
						map[string]string{
							"content_type": "location",
						},
						map[string]string{
							"content_type": "text",
							"title":        "取消",
							"payload":      "CANCEL",
						},
					})
				case "GET_STARTED":
					fsmErr = user.FSM.Event("greeting")
					err = a.SendText(senderId, WELCOME_TEXT)
				default:
					fsmErr = user.FSM.Event("cancel")
				}
			}
		case *ambassador.FBMessageDelivery, *ambassador.FBMessageRead:
			log.Debugf(ctx, "Ignored message: %+v", msgBody)
		default:
			log.Debugf(ctx, "Receive unhandled message: %+v", msgBody)
		}

		// Handle errors for every state changing and message delivery
		if fsmErr != nil {
			switch fsmErr.(type) {
			case *fsm.NoTransitionError:
				log.Warningf(ctx, fsmErr.Error())
			default:
				log.Errorf(ctx, "unexpected state machine error: %s", fsmErr.Error())
			}
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
