package cafehunter

type Cafe struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	City string `json:"city"`

	Wifi  float64 `json:"wifi"`
	Seat  float64 `json:"seat"`
	Quiet float64 `json:"quiet"`
	Tasty float64 `json:"tasty"`
	Price float64 `json:"cheap"`
	Music float64 `json:"music"`

	TimeLimited string `json:"timeLimited"`
	Plug        string `json:"plug"`

	Address   string  `json:"address"`
	Link      string  `json:"url"`
	Latitude  float64 `json:"latitude,string"`
	Longitude float64 `json:"longitude,string"`
	Geohash   string  `json:"geohash"`
}
