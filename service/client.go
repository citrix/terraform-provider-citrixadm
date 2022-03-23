package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/go-hclog"
)

type NitroRequestParams struct {
	ResourcePath       string
	Method             string
	Headers            map[string]string
	Resource           string
	ResourceData       interface{}
	ResourceID         string
	SuccessStatusCodes []int
}

//NitroParams encapsulates options to create a NitroClient
type NitroParams struct {
	Host         string
	HostLocation string
	ID           string
	Secret       string
	CustomerID   string
	// SslVerify     bool
	// Timeout       int
	// RootCAPath    string
	// ServerName    string
	Headers       map[string]string
	LogLevel      string
	JSONLogFormat bool
}

//NitroClient has methods to configure the NetScaler
//It abstracts the REST operations of the NITRO API
type NitroClient struct {
	host         string
	hostLocation string
	id           string
	secret       string
	client       *http.Client
	customerid   string
	// sessionidMux sync.RWMutex
	// sessionid    string
	// timeout      int
	headers     map[string]string
	logger      hclog.Logger
	accessToken string
}

//NewNitroClientFromParams returns a usable NitroClient. Does not check validity of supplied parameters
func NewNitroClientFromParams(params NitroParams) (*NitroClient, error) {
	u, err := url.Parse(params.Host)
	if err != nil {
		return nil, fmt.Errorf("Supplied URL %s is not a URL", params.Host)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("Supplied Host %s does not have a HTTP/HTTPS scheme", params.Host)
	}
	c := new(NitroClient)
	c.host = params.Host
	c.id = params.ID
	c.secret = params.Secret
	c.headers = params.Headers
	c.hostLocation = params.HostLocation
	c.customerid = params.CustomerID
	c.client = &http.Client{}

	// Get New Token
	if err := c.GetNewToken(); err != nil {
		return nil, err
	}
	// c.sessionid = ""
	// c.timeout = params.Timeout

	level := hclog.LevelFromString(params.LogLevel)
	if level == hclog.NoLevel {
		level = hclog.Off
	}
	logLevel, ok := os.LookupEnv("NS_LOG")
	if ok {
		lvl := hclog.LevelFromString(logLevel)
		if lvl != hclog.NoLevel {
			level = lvl
		} else {
			log.Printf("nitro-go: NS_LOG not set to a valid log level (%s), defaulting to %d", logLevel, level)
		}
	}
	//c.logger = hclog.Default()
	c.logger = hclog.New(&hclog.LoggerOptions{
		Name:            "citrixadm-client",
		Level:           level,
		Color:           hclog.AutoColor,
		JSONFormat:      params.JSONLogFormat,
		IncludeLocation: true,
	})
	return c, nil
}

// GetNewToken returns a new access token
func (c *NitroClient) GetNewToken() error {
	endpoint := fmt.Sprintf("https://api-%s.cloud.com/cctrustoauth2/root/tokens/clients", c.hostLocation)

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", c.id)
	data.Set("client_secret", c.secret)

	client := &http.Client{}
	r, err := http.NewRequest("POST", endpoint, strings.NewReader(data.Encode())) // URL-encoded payload
	if err != nil {
		log.Fatal(err)
	}
	r.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Add("Accept", "application/json")

	res, err := client.Do(r)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(res.Status)
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(string(body))
	response := map[string]interface{}{}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		panic(err)
	}
	// log.Println(sec["access_token"].(string))
	c.accessToken = response["access_token"].(string)
	return nil
}

// MakeNitroRequest makes a API request to the NetScaler
func (c *NitroClient) MakeNitroRequest(n NitroRequestParams) ([]byte, error) {
	var buff []byte
	var err error

	if n.Method == "POST" || n.Method == "PUT" {
		n.ResourcePath = fmt.Sprintf("massvc/%s/nitro/v2/config/%s", c.customerid, n.Resource)
		payload := map[string]interface{}{n.Resource: n.ResourceData}
		buff, err = JSONMarshal(payload)
		if err != nil {
			return nil, err
		}
	} else if n.Method == "GET" || n.Method == "DELETE" {
		n.ResourcePath = fmt.Sprintf("massvc/%s/nitro/v2/config/%s/%s", c.customerid, n.Resource, n.ResourceID)
		buff = []byte{}
	}

	urlstr := fmt.Sprintf("%s/%s", c.host, n.ResourcePath)

	req, err := http.NewRequest(n.Method, urlstr, bytes.NewBuffer(buff))
	if err != nil {
		return nil, err
	}

	// Authenticate
	req.Header.Set("Authorization", fmt.Sprintf("CwsAuth bearer=%s", c.accessToken))

	// Standard headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("isCloud", "true")

	// Request defined headers
	log.Println("MakeNitroRequest method: Request defined headers")
	for k, v := range n.Headers {
		log.Println(k, v)
		req.Header.Set(k, v)
	}

	log.Printf("MakeNitroRequest method:%s, url:%s, headers:%v", req.Method, req.URL, req.Header)

	resp, err := c.client.Do(req)

	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	var body []byte

	if statusCodeSuccess(n.SuccessStatusCodes, resp.StatusCode) {
		body, _ = ioutil.ReadAll(resp.Body)
		return body, nil
	}
	body, _ = ioutil.ReadAll(resp.Body)
	return []byte{}, errors.New("failed: " + resp.Status + " (" + string(body) + ")")
}

func statusCodeSuccess(slice []int, val int) bool {
	for _, item := range slice {
		if val == item {
			return true
		}
	}
	return false
}

// JSONMarshal https://stackoverflow.com/questions/28595664/how-to-stop-json-marshal-from-escaping-and/28596225#28596225
func JSONMarshal(t interface{}) ([]byte, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(t)
	return buffer.Bytes(), err
}
