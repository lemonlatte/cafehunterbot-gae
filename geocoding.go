package cafehunter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type ReverseResult struct {
	Id          string  `json:"place_id"`
	Type        string  `json:"osm_type"`
	Latitude    float64 `json:"lat,string"`
	Longitude   float64 `json:"lon,string"`
	DisplayName string  `json:"display_name"`
	Address     Address `json:"address"`
}

type Address struct {
	Postbox      string `json:"post_box"`
	Convenience  string `json:"convenience"`
	HouseNumber  string `json:"house_number"`
	Road         string `json:"road"`
	CityDistrict string `json:"city_district"`
	Hamlet       string `json:"hamlet"`
	Suburb       string `json:"suburb"`
	State        string `json:"state"`
	Postcode     string `json:"postcode"`
	Country      string `json:"country"`
	CountryCode  string `json:"country_code"`
}

type GoogleGeometry struct {
	Location     GoogleLocation `json:"location"`
	LocationType string         `json:"location_type"`
}

type GoogleLocation struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

type GoogleGeocodingResult struct {
	FormattedAddress string         `json:"formatted_address"`
	Geometry         GoogleGeometry `json:"geometry"`
}

type GoogleGeocodingBody struct {
	Results []GoogleGeocodingResult `json:"results"`
	Status  string                  `json:"status"`
}

type GoogleMapApiClient struct {
	apiKey string
}

func (c *GoogleMapApiClient) getGeocoding(request func(*http.Request) (*http.Response, error), address string) (lat, long float64, err error) {
	u := url.URL{
		Scheme: "https",
		Host:   "maps.googleapis.com",
		Path:   "/maps/api/geocode/json",
		RawQuery: fmt.Sprintf("address=%s&key=%s",
			address, c.apiKey),
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

	geoBody := GoogleGeocodingBody{}

	d := json.NewDecoder(resp.Body)
	err = d.Decode(&geoBody)

	if err != nil {
		return
	}

	if len(geoBody.Results) == 0 {
		err = fmt.Errorf("no results found for address: %s", address)
		return
	}

	g := geoBody.Results[0]
	lat = g.Geometry.Location.Latitude
	long = g.Geometry.Location.Longitude
	return
}

func getAddress(request func(*http.Request) (*http.Response, error), lat, long float64) (r ReverseResult, err error) {
	u := url.URL{
		Scheme: "http",
		Host:   "nominatim.openstreetmap.org",
		Path:   "/reverse",
		RawQuery: fmt.Sprintf(
			"format=json&lat=%f&lon=%f&addressdetails=1&accept-language=zh-tw",
			lat, long,
		),
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
