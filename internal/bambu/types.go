package bambu

// Response shapes for the Bambu Cloud REST API.
//
// The API is unofficial and undocumented. Every field below was confirmed
// against a live X2D account; fields the X2D leaves null are pointers or have
// documented fallbacks at the point of use. Anything not listed is
// deliberately ignored rather than speculatively mapped.

// TasksResponse is GET /user-service/my/tasks.
type TasksResponse struct {
	Total int    `json:"total"`
	Hits  []Task `json:"hits"`
}

// Task is one print job from the account's history.
type Task struct {
	ID int64 `json:"id"`
	// Status codes confirmed against this account's own history:
	// 2 = finished cleanly, 3 = failed. Anything else is treated as running
	// rather than guessed at.
	Status           int         `json:"status"`
	Title            string      `json:"title"`
	DesignTitle      string      `json:"designTitle"`
	DesignID         int64       `json:"designId"`
	StartTime        string      `json:"startTime"`
	CostTime         int         `json:"costTime"` // seconds
	Weight           float64     `json:"weight"`   // grams
	AMSDetailMapping []AMSDetail `json:"amsDetailMapping"`
}

// AMSDetail is per-filament usage within a single print. This is the only
// place the API reports which colour/material a job actually consumed.
type AMSDetail struct {
	TargetColor  string  `json:"targetColor"`
	FilamentType string  `json:"filamentType"`
	Weight       float64 `json:"weight"`
}

// FilamentResponse is GET /design-user-service/my/filament/v2. Only spools
// registered with the AMS via RFID appear here — sealed spares are invisible.
type FilamentResponse struct {
	Hits []Filament `json:"hits"`
}

// Filament is one RFID-registered spool.
type Filament struct {
	FilamentName   string  `json:"filamentName"`
	FilamentType   string  `json:"filamentType"`
	Color          string  `json:"color"`
	NetWeight      float64 `json:"netWeight"`      // remaining grams (AMS estimate, drifts)
	TotalNetWeight float64 `json:"totalNetWeight"` // spool capacity
	AMSID          int     `json:"amsId"`
	SlotID         any     `json:"slotId"` // int or "" depending on load state
	InPrinter      bool    `json:"inPrinter"`
	Depleted       bool    `json:"depleted"`
}

// PreferenceResponse is GET /design-user-service/my/preference — used only to
// resolve the MakerWorld uid needed by the favourites endpoint.
type PreferenceResponse struct {
	UID int64 `json:"uid"`
}

// FavoritesResponse is GET /design-service/favorites/designs/{uid}.
type FavoritesResponse struct {
	Hits []Design `json:"hits"`
}

// Design is one favourited MakerWorld model.
type Design struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	PrintCount int    `json:"printCount"`
	Creator    struct {
		Name string `json:"name"`
	} `json:"designCreator"`
}
