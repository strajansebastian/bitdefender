package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/crackcomm/go-clitable"
	"github.com/fatih/structs"
	"github.com/gorilla/mux"
	"github.com/maliceio/go-plugin-utils/database/elasticsearch"
	"github.com/maliceio/go-plugin-utils/utils"
	"github.com/parnurzeal/gorequest"
	"github.com/urfave/cli"
)

// Version stores the plugin's version
var Version string

// BuildTime stores the plugin's build time
var BuildTime string

const (
	name     = "bitdefender"
	category = "av"
)

type pluginResults struct {
	ID   string      `json:"id" structs:"id,omitempty"`
	Data ResultsData `json:"bitdefender" structs:"bitdefender"`
}

// Bitdefender json object
type Bitdefender struct {
	Results ResultsData `json:"bitdefender"`
}

// ResultsData json object
type ResultsData struct {
	Infected bool   `json:"infected" structs:"infected"`
	Result   string `json:"result" structs:"result"`
	Engine   string `json:"engine" structs:"engine"`
	Updated  string `json:"updated" structs:"updated"`
}

// AvScan performs antivirus scan
func AvScan(path string, timeout int) Bitdefender {

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	return Bitdefender{
		Results: ParseBitdefenderOutput(utils.RunCommand(ctx, "bdscan", path)),
	}
}

// ParseBitdefenderOutput convert bitdefender output into ResultsData struct
func ParseBitdefenderOutput(bitdefenderout string, err error) ResultsData {

	if err != nil {
		return ResultsData{}
	}

	bitdefender := ResultsData{Infected: false}
	// EXAMPLE OUTPUT:
	// BitDefender Antivirus Scanner for Unices v7.90123 Linux-amd64
	// Copyright (C) 1996-2009 BitDefender. All rights reserved.
	// Trial key found. 30 days remaining.
	//
	// Infected file action: ignore
	// Suspected file action: ignore
	// Loading plugins, please wait
	// Plugins loaded.
	//
	// /malware/EICAR  infected: EICAR-Test-File (not a virus)
	//
	//
	// Results:
	// Folders: 0
	// Files: 1
	// Packed: 0
	// Archives: 0
	// Infected files: 1
	// Suspect files: 0
	// Warnings: 0
	// Identified viruses: 1
	// I/O errors: 0
	lines := strings.Split(bitdefenderout, "\n")

	// Extract Virus string
	for _, line := range lines {
		if len(line) != 0 {
			switch {
			case strings.Contains(line, "infected:"):
				result := extractVirusName(line)
				if len(result) != 0 {
					bitdefender.Result = result
					bitdefender.Infected = true
				} else {
					fmt.Println("[ERROR] Virus name extracted was empty: ", result)
					os.Exit(2)
				}
			case strings.Contains(line, "Unices v"):
				words := strings.Fields(line)
				for _, word := range words {
					if strings.HasPrefix(word, "v") {
						bitdefender.Engine = strings.TrimPrefix(word, "v")
					}
				}
			}
		}
	}

	bitdefender.Updated = getUpdatedDate()

	return bitdefender
}

// extractVirusName extracts Virus name from scan results string
func extractVirusName(line string) string {
	keyvalue := strings.Split(line, "infected:")
	return strings.TrimSpace(keyvalue[1])
}

func getUpdatedDate() string {
	if _, err := os.Stat("/opt/malice/UPDATED"); os.IsNotExist(err) {
		return BuildTime
	}
	updated, err := ioutil.ReadFile("/opt/malice/UPDATED")
	utils.Assert(err)
	return string(updated)
}

func updateAV(ctx context.Context) error {
	fmt.Println("Updating Bitdefender...")
	fmt.Println(utils.RunCommand(ctx, "bdscan", "--update"))
	// Update UPDATED file
	t := time.Now().Format("20060102")
	err := ioutil.WriteFile("/opt/malice/UPDATED", []byte(t), 0644)
	return err
}

func printMarkDownTable(bitdefender Bitdefender) {

	fmt.Println("#### Bitdefender")
	table := clitable.New([]string{"Infected", "Result", "Engine", "Updated"})
	table.AddRow(map[string]interface{}{
		"Infected": bitdefender.Results.Infected,
		"Result":   bitdefender.Results.Result,
		"Engine":   bitdefender.Results.Engine,
		"Updated":  bitdefender.Results.Updated,
	})
	table.Markdown = true
	table.Print()
}

func printStatus(resp gorequest.Response, body string, errs []error) {
	fmt.Println(resp.Status)
}

func webService() {
	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/scan", webAvScan).Methods("POST")
	log.Info("web service listening on port :3993")
	log.Fatal(http.ListenAndServe(":3993", router))
}

func webAvScan(w http.ResponseWriter, r *http.Request) {

	r.ParseMultipartForm(32 << 20)
	file, header, err := r.FormFile("malware")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, "Please supply a valid file to scan.")
		log.Error(err)
	}
	defer file.Close()

	log.Debug("Uploaded fileName: ", header.Filename)

	tmpfile, err := ioutil.TempFile("/malware", "web_")
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(tmpfile.Name()) // clean up

	data, err := ioutil.ReadAll(file)

	if _, err = tmpfile.Write(data); err != nil {
		log.Fatal(err)
	}
	if err = tmpfile.Close(); err != nil {
		log.Fatal(err)
	}

	// Do AV scan
	bitdefender := AvScan(tmpfile.Name(), 60)

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(bitdefender); err != nil {
		log.Fatal(err)
	}
}

func main() {

	var elastic string

	cli.AppHelpTemplate = utils.AppHelpTemplate
	app := cli.NewApp()

	app.Name = "bitdefender"
	app.Author = "blacktop"
	app.Email = "https://github.com/blacktop"
	app.Version = Version + ", BuildTime: " + BuildTime
	app.Compiled, _ = time.Parse("20060102", BuildTime)
	app.Usage = "Malice Bitdefender AntiVirus Plugin"
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "verbose, V",
			Usage: "verbose output",
		},
		cli.BoolFlag{
			Name:  "table, t",
			Usage: "output as Markdown table",
		},
		cli.BoolFlag{
			Name:   "callback, c",
			Usage:  "POST results to Malice webhook",
			EnvVar: "MALICE_ENDPOINT",
		},
		cli.BoolFlag{
			Name:   "proxy, x",
			Usage:  "proxy settings for Malice webhook endpoint",
			EnvVar: "MALICE_PROXY",
		},
		cli.StringFlag{
			Name:        "elasitcsearch",
			Value:       "",
			Usage:       "elasitcsearch address for Malice to store results",
			EnvVar:      "MALICE_ELASTICSEARCH",
			Destination: &elastic,
		},
		cli.IntFlag{
			Name:   "timeout",
			Value:  60,
			Usage:  "malice plugin timeout (in seconds)",
			EnvVar: "MALICE_TIMEOUT",
		},
	}
	app.Commands = []cli.Command{
		{
			Name:    "update",
			Aliases: []string{"u"},
			Usage:   "Update virus definitions",
			Action: func(c *cli.Context) error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.Int("timeout"))*time.Second)
				defer cancel()
				return updateAV(ctx)
			},
		},
		{
			Name:  "web",
			Usage: "Create a Bitdefender scan web service",
			Action: func(c *cli.Context) error {
				// ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.Int("timeout"))*time.Second)
				// defer cancel()

				webService()

				return nil
			},
		},
	}
	app.Action = func(c *cli.Context) error {

		if c.Bool("verbose") {
			log.SetLevel(log.DebugLevel)
		}

		if c.Args().Present() {
			path, err := filepath.Abs(c.Args().First())
			utils.Assert(err)

			if _, err := os.Stat(path); os.IsNotExist(err) {
				utils.Assert(err)
			}

			bitdefender := AvScan(path, c.Int("timeout"))

			// upsert into Database
			elasticsearch.InitElasticSearch(elastic)
			elasticsearch.WritePluginResultsToDatabase(elasticsearch.PluginResults{
				ID:       utils.Getopt("MALICE_SCANID", utils.GetSHA256(path)),
				Name:     name,
				Category: category,
				Data:     structs.Map(bitdefender.Results),
			})

			if c.Bool("table") {
				printMarkDownTable(bitdefender)
			} else {
				bitdefenderJSON, err := json.Marshal(bitdefender)
				utils.Assert(err)
				if c.Bool("post") {
					request := gorequest.New()
					if c.Bool("proxy") {
						request = gorequest.New().Proxy(os.Getenv("MALICE_PROXY"))
					}
					request.Post(os.Getenv("MALICE_ENDPOINT")).
						Set("X-Malice-ID", utils.Getopt("MALICE_SCANID", utils.GetSHA256(path))).
						Send(string(bitdefenderJSON)).
						End(printStatus)

					return nil
				}
				fmt.Println(string(bitdefenderJSON))
			}
		} else {
			log.Fatal(fmt.Errorf("Please supply a file to scan with malice/bitdefender"))
		}
		return nil
	}

	err := app.Run(os.Args)
	utils.Assert(err)
}