package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/oui"
)

var db oui.DynamicDB
var UpdateAt *time.Time
var mu sync.RWMutex
var loadWait *sync.Cond
var updating bool

const dbUrl = "http://standards-oui.ieee.org/oui.txt"

func main() {
	http.HandleFunc("/_ah/warmup", warmupHandler)
	http.HandleFunc("/", handler)

	// [START setting_port]
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
	// [END setting_port]
}

// Inital loading of DB.
func start(c context.Context) error {
	var err error

	loadWait = sync.NewCond(&mu)
	log.Printf("Loading db...")
	client := createClient(c, time.Second*30)
	resp, err := client.Get(dbUrl)
	if err != nil {
		log.Printf("Error downloading:%s", err.Error())
		return err
	}
	defer resp.Body.Close()
	db, err = oui.Open(resp.Body)

	if err != nil {
		log.Printf("Error parsing:%s", err.Error())
		return err
	}
	t := time.Now().Add(time.Hour * 24)
	UpdateAt = &t
	log.Printf("Loaded, now serving...")
	loadWait.Broadcast()
	return nil
}

// Update DB - happens at a user request
// - could be done via a specific URL.
func update(c context.Context) {
	var err error
	log.Printf("Updating DB on instance...")
	client := createClient(c, time.Second*30)
	resp, err := client.Get(dbUrl)
	if err != nil {
		log.Printf("Error downloading:%s", err.Error())
		return
	}
	defer resp.Body.Close()
	err = oui.Update(db, resp.Body)

	if err != nil {
		log.Printf("Error parsing:%s", err.Error())
		return
	}
	t := time.Now().Add(time.Hour * 24)
	UpdateAt = &t
	log.Printf("Updated database...")
}

var startOnce sync.Once

type Response struct {
	Data  *oui.Entry `json:"data,omitempty"`
	Error string     `json:"error,omitempty"`
}

// Default handler
func handler(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	// Load db on first request.
	var err error
	err = nil
	startOnce.Do(func() {
		err = start(c)
	})
	if err != nil {
		startOnce = sync.Once{}
		log.Printf("unable to load db:" + err.Error())
	}
	if UpdateAt == nil {
		loadWait.Wait()
	}
	if UpdateAt.Before(time.Now()) && !updating {
		updating = true
		update(c)
		updating = false
	}
	var mac string
	var hw *oui.HardwareAddr

	// Prepare the response and queue sending the result.
	res := &Response{}

	defer func() {
		var j []byte
		var err error
		j, err = json.Marshal(&res.Data)
		if err != nil {
			log.Printf(err.Error())
			return
		}
		w.Write(j)
	}()

	// Set headers
	w.Header().Set("Cache-Control", "public, max-age=86400") // 86400 = 24*60*60
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Expires", UpdateAt.Format(http.TimeFormat))
	w.Header().Set("Last-Modified", db.Generated().Format(http.TimeFormat))

	mac = r.URL.Query().Get("mac")
	if mac == "" {
		mac = strings.Trim(r.URL.Path, "/")
	}
	hw, err = oui.ParseMac(mac)
	if err != nil {
		res.Error = err.Error() + ". Usage 'https://<host>/AB-CD-EF' (dashes can be colons or omitted)."
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	entry, err := db.LookUp(*hw)
	if err != nil {
		if err == oui.ErrNotFound {
			res.Error = "not found in db"
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		res.Error = err.Error()
		return
	}
	res.Data = entry

}

func warmupHandler(w http.ResponseWriter, r *http.Request) {
	c := r.Context()
	var err error
	err = nil
	startOnce.Do(func() {
		err = start(c)
	})
	if err != nil {
		startOnce = sync.Once{}
		log.Printf("unable to load db:" + err.Error())
	}
}

// createClient is urlfetch.Client with Deadline
func createClient(context context.Context, t time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{},
	}
}
