package cafehunter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const LUIS_URL = "api.projectoxford.ai"
const APP_ID = ""
const APP_KEY = ""

type Intent struct {
	Intent string  `json:"intent"`
	Score  float64 `json:"score"`
}

type Entity struct {
	Entity string  `json:"entity"`
	Type   string  `json:"type"`
	Score  float64 `json:"score"`
}

type LuisResult struct {
	Query            string
	TopScoringIntent Intent   `json:"topScoringIntent"`
	Entities         []Entity `json:"entities"`
}

func fetchIntent(request func(*http.Request) (*http.Response, error), query string, verbose bool) (r LuisResult, err error) {
	v := "false"
	if verbose {
		v = "true"
	}

	u := url.URL{
		Scheme:   "https",
		Host:     LUIS_URL,
		Path:     fmt.Sprintf("/luis/v2.0/apps/%s", APP_ID),
		RawQuery: fmt.Sprintf("subscription-key=%s&q=%s&verbose=%s", APP_KEY, url.QueryEscape(query), v),
	}

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return
	}
	resp, err := request(req)
	if resp.StatusCode != 200 {
		return
	}
	defer resp.Body.Close()

	d := json.NewDecoder(resp.Body)
	err = d.Decode(&r)
	return
}
