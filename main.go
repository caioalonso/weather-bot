package main

import (
	"bytes"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/k0kubun/pp"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly"
	"github.com/gosimple/slug"
	_ "github.com/mattn/go-sqlite3"
	tb "gopkg.in/tucnak/telebot.v2"
)

//curl 'https://www.cptec.inpe.br/autocomplete?term=bom%20jesus%20dos%20perd'
//-H 'User-Agent: Mozilla/5.0 (X11; Linux x86_64; rv:63.0) Gecko/20100101 Firefox/63.0'
//-H 'Accept: application/json, text/javascript, */*; q=0.01' -H 'Accept-Language: en-US,en;q=0.5'
//--compressed -H 'Referer: https://www.cptec.inpe.br/' -H 'X-Requested-With: XMLHttpRequest'
//-H 'Connection: keep-alive'
//-H 'Cookie: XSRF-TOKEN=eyJpdiI6Ikx1RXdcL1gzWHhyYU9JYWdhYWt5S2t3PT0iLCJ2YWx1ZSI6InFZazVRMGtUZnFITThHTTkzT0dmVVFqNmlCVnBtNWJZZW1Va3gwT1FJbFwvMWhkbyt6XC9aNXhseUtzZG9lYVJSd093QjhnQWllTU55S0tjUTJHaUpvQkE9PSIsIm1hYyI6ImMzOGM2YTM0MDcxNTdhNTFlODg5NjhjN2Q4YjIwODI4YmU0MWVkN2VlNGQ0Yzc4Y2Q1MzhjOWQ2MmYzYTcwZDEifQ%3D%3D; portal_cptec_session=eyJpdiI6InM2U010K3hFVGpCSURtTW9yN0o1RlE9PSIsInZhbHVlIjoiQmt0T0dRU2Q4cVpBdlpCUmVmNTNLMmtHNUJsWFdsWk9rb1dxNkNZQ2d2Zk03cWdGd2tMSHd2Szh1T0czaWVSRTJsR0E3TUM1end3M0Rsek9ONStlTWc9PSIsIm1hYyI6ImIwZjM3MzgwYjNhMTA4NDE0MmVhNzg5MDNmYWQzOWRkMDQ5YTdkYzFiNjI0ZmFlMGY1MmVjNTFjNWI1NDU2MWQifQ%3D%3D'
//-H 'Pragma: no-cache' -H 'Cache-Control: no-cache'

func normalizeName(s string) string {
	t := transform.Chain(
		norm.NFD,
		transform.RemoveFunc(func(r rune) bool {
			return unicode.Is(unicode.Mn, r)
		}),
		norm.NFC,
	)
	normalizedName, _, _ := transform.String(t, s)
	return strings.ToLower(normalizedName)
}

type City struct {
	ID    int    `xml:"id"`
	Name  string `xml:"nome"`
	State string `xml:"uf"`
}

type Result struct {
	Cities []*City `xml:"cidade"`
}

type Forecast struct {
	Day         string `xml:"dia"`
	Climate     string `xml:"tempo"`
	Description string
	Max         string `xml:"maxima"`
	Min         string `xml:"minima"`
	UV          string `xml:"iuv"`
}

type ForecastResult struct {
	Name      string      `xml:"nome"`
	State     string      `xml:"uf"`
	Forecasts []*Forecast `xml:"previsao"`
}

func getCPTECCities(s string) (cities []*City, err error) {
	requestURL := "http://servicos.cptec.inpe.br/XML/listaCidades?city=" + url.QueryEscape(s)
	resp, err := http.Get(requestURL)
	if err != nil {
		return
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	result := Result{}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.CharsetReader = charset.NewReaderLabel
	err = decoder.Decode(&result)
	if err != nil {
		return
	}
	cities = result.Cities
	return
}

func getForecast(city *City) (result *ForecastResult, err error) {
	requestURL := fmt.Sprintf("http://servicos.cptec.inpe.br/XML/cidade/%v/previsao.xml", city.ID)
	fmt.Println("GET %s", requestURL)
	resp, err := http.Get(requestURL)
	if err != nil {
		return
	}

	defer resp.Body.Close()

	decoder := xml.NewDecoder(resp.Body)
	decoder.CharsetReader = charset.NewReaderLabel
	err = decoder.Decode(&result)
	if err != nil {
		return
	}
	today, err := getTodayForecast(city)
	if err != nil {
		return
	}
	result.Forecasts = append([]*Forecast{today}, result.Forecasts...)
	return
}

// todays forecast can only be scraped from their site
func getTodayForecast(city *City) (forecast *Forecast, err error) {
	requestURL := fmt.Sprintf(
		"https://www.cptec.inpe.br/previsao-tempo/%s/%s",
		strings.ToLower(city.State),
		slug.Make(city.Name),
	)
	fmt.Println("GET %s", requestURL)
	resp, err := http.Get(requestURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return
	}

	forecast = &Forecast{}

	imageSrc, _ := doc.Find("img.img-responsive.center-block").First().Attr("src")
	imageSrcSplit := strings.Split(imageSrc, "/")
	imageSrc = strings.Split(imageSrcSplit[len(imageSrcSplit)-1], ".")[0]
	forecast.Climate = strings.Split(imageSrcSplit[len(imageSrcSplit)-1], "_")[0]

	forecast.Description = strings.TrimSpace(doc.Find("div.col-md-12 > div.d-flex > div.p-2.text-center").First().Text())

	forecast.Max = doc.Find("div.temperaturas span.text-danger").First().Text()
	forecast.Max = strings.Replace(forecast.Max, "°", "", -1)

	forecast.Min = doc.Find("div.temperaturas span.text-primary").First().Text()
	forecast.Min = strings.Replace(forecast.Min, "°", "", -1)

	uvSelection := doc.Find("div.col-md-12 > div.row.align-middle.justify-content-md-center").Eq(2)
	forecast.UV = uvSelection.Find("div.col-md-4 span").Last().Text()

	return
}

func buildDoc(r *colly.Response) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(bytes.NewReader(r.Body))
}

func getListOfCities() (cities []string, err error) {
	rows, err := db.Query("select id, name from ibge")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var name string
		err = rows.Scan(&id, &name)
		if err != nil {
			log.Fatal(err)
		}
		cities = append(cities, name)
	}
	err = rows.Err()
	return
}

func addCPTECCitiesToDB(cities []*City) {
	for _, city := range cities {
		_, err := db.Exec("insert into cptec(id, name, state) values(?,?,?)", city.ID, city.Name, city.State)
		if err != nil {
			log.Panic(err)
		}
	}
	return
}

var db *sql.DB
var b *tb.Bot

func main() {
	isBuild := len(os.Args) > 1 && os.Args[1:][0] == "build"

	var err error
	db, err = sql.Open("sqlite3", "./cities.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if isBuild {
		cities, err := getListOfCities()
		if err != nil {
			log.Panic(err)
		}
		fmt.Println(len(cities))

		for _, city := range cities {
			pp.Println(city)
			CPTECCities, err := getCPTECCities(normalizeName(city))
			if err != nil {
				log.Panic(err)
			}
			addCPTECCitiesToDB(CPTECCities)
		}
	} else {
		b, err = tb.NewBot(tb.Settings{
			Token:  "TELEGRAM TOKEN HERE",
			Poller: &tb.LongPoller{Timeout: 10 * time.Second},
		})

		if err != nil {
			log.Fatal(err)
			return
		}

		b.Handle("/weather", replyForecast)
		b.Handle("/clima", replyForecast)
		b.Handle("/forecast", replyForecast)
		b.Handle("/previsao", replyForecast)
		b.Handle(tb.OnText, replyForecast)

		b.Start()
	}
}

func replyForecast(m *tb.Message) {
	city, err := getCity(m.Payload)
	if err != nil {
		b.Send(m.Chat, "Não encontrei este município.")
		return
	}
	forecast, err := getForecast(city)
	if err != nil {
		b.Send(m.Chat, fmt.Sprintf("Erro ao obter previsão: %s", err))
		return
	}

	b.Send(m.Chat, forecastString(forecast), &tb.SendOptions{
		ParseMode: tb.ModeMarkdown,
	})
}

func forecastString(f *ForecastResult) (s string) {
	s = fmt.Sprintf("%s, %s\n", f.Name, f.State)
	s += singleForecastString(f.Forecasts[0], "Hoje")
	s += singleForecastString(f.Forecasts[1], "Amanhã")
	s += singleForecastString(f.Forecasts[2], "Depois de amanhã")
	return
}

func singleForecastString(f *Forecast, day string) string {
	return fmt.Sprintf(
		"*%s*: %s\nMín. %sºC, Máx. %sºC, UV %s\n",
		day,
		friendlyClimate(f),
		f.Min,
		f.Max,
		f.UV,
	)
}

func friendlyClimate(f *Forecast) string {
	climateMap := map[string]string{
		"ec":  "Encoberto com Chuvas Isoladas",
		"ci":  "Chuvas Isoladas",
		"c":   "Chuva",
		"in":  "Instável",
		"pp":  "Poss. de Pancadas de Chuva",
		"cm":  "Chuva pela Manhã",
		"cn":  "Chuva a Noite",
		"pt":  "Pancadas de Chuva a Tarde",
		"pm":  "Pancadas de Chuva pela Manhã",
		"np":  "Nublado e Pancadas de Chuva",
		"pc":  "Pancadas de Chuva",
		"pn":  "Parcialmente Nublado",
		"cv":  "Chuvisco",
		"ch":  "Chuvoso",
		"t":   "Tempestade",
		"ps":  "Predomínio de Sol",
		"e":   "Encoberto",
		"n":   "Nublado",
		"cl":  "Céu Claro",
		"nv":  "Nevoeiro",
		"g":   "Geada",
		"ne":  "Neve",
		"nd":  "Não Definido",
		"pnt": "Pancadas de Chuva a Noite",
		"psc": "Possibilidade de Chuva",
		"pcm": "Possibilidade de Chuva pela Manhã",
		"pct": "Possibilidade de Chuva a Tarde",
		"pcn": "Possibilidade de Chuva a Noite",
		"npt": "Nublado com Pancadas a Tarde",
		"npn": "Nublado com Pancadas a Noite",
		"ncn": "Nublado com Poss. de Chuva a Noite",
		"nct": "Nublado com Poss. de Chuva a Tarde",
		"ncm": "Nubl. c/ Poss. de Chuva pela Manhã",
		"npm": "Nublado com Pancadas pela Manhã",
		"npp": "Nublado com Possibilidade de Chuva",
		"vn":  "Variação de Nebulosidade",
		"ct":  "Chuva a Tarde",
		"ppn": "Poss. de Panc. de Chuva a Noite",
		"ppt": "Poss. de Panc. de Chuva a Tarde",
		"ppm": "Poss. de Panc. de Chuva pela Manhã",
	}

	emojiMap := map[string]string{
		"ec":  "🌦",
		"ci":  "🌦",
		"c":   "🌧",
		"in":  "🌦",
		"pp":  "🌦",
		"cm":  "🌧",
		"cn":  "🌧",
		"pt":  "🌦",
		"pm":  "🌦",
		"np":  "🌦",
		"pc":  "🌦",
		"pn":  "🌤",
		"cv":  "🌧",
		"ch":  "🌧",
		"t":   "⛈",
		"ps":  "☀",
		"e":   "⛅",
		"n":   "🌥",
		"cl":  "☀",
		"nv":  "🌫",
		"g":   "❄",
		"ne":  "☃",
		"nd":  "",
		"pnt": "🌧",
		"psc": "🌧",
		"pcm": "🌧",
		"pct": "🌧",
		"pcn": "🌧",
		"npt": "🌧",
		"npn": "🌧",
		"ncn": "🌧",
		"nct": "🌧",
		"ncm": "🌧",
		"npm": "🌧",
		"npp": "🌧",
		"vn":  "🌥",
		"ct":  "🌧",
		"ppn": "🌧",
		"ppt": "🌧",
		"ppm": "🌧",
	}
	if f.Description != "" {
		return emojiMap[f.Climate] + " " + f.Description
	}
	return emojiMap[f.Climate] + " " + climateMap[f.Climate]
}

func getCity(str string) (city *City, err error) {
	city = &City{}
	stmt, err := db.Prepare("select ID, Name, State from cptec where cptec = ?")
	if err != nil {
		return
	}
	defer stmt.Close()
	err = stmt.QueryRow(str).Scan(&city.ID, &city.Name, &city.State)
	if err != nil {
		return
	}
	return
}
