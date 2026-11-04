package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// publicIP mirrors gluetun's models.PublicIP JSON, plus "protected".
type publicIP struct {
	IP           string `json:"public_ip"`
	Region       string `json:"region,omitempty"`
	Country      string `json:"country,omitempty"`
	City         string `json:"city,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	Location     string `json:"location,omitempty"`
	Organization string `json:"organization,omitempty"`
	PostalCode   string `json:"postal_code,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	Protected    *bool  `json:"protected,omitempty"`
}

const publicIPMaxAge = time.Hour

type publicIPFetcher struct {
	enabled bool
	api     string
	token   string
	client  *http.Client

	mutex     sync.Mutex
	cached    publicIP
	cacheKey  string
	fetchedAt time.Time
}

func newPublicIPFetcher(enabled bool, api, token string) *publicIPFetcher {
	return &publicIPFetcher{
		enabled: enabled,
		api:     strings.ToLower(api),
		token:   token,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Get returns the public IP information. It is only fetched while the VPN is
// connected: the kill switch lets HTTPS out of eth0 for the NordVPN API, so
// a lookup made while disconnected could reveal the real IP. The result is
// cached until the VPN server changes or an hour has passed.
func (p *publicIPFetcher) Get(ctx context.Context, vpn *vpnController) (publicIP, error) {
	if !p.enabled {
		return publicIP{}, nil
	}

	status, err := vpn.nordStatus(ctx)
	if err != nil {
		return publicIP{}, err
	}
	if state, _ := status["status"].(string); !strings.EqualFold(state, "connected") {
		p.mutex.Lock()
		p.cached, p.cacheKey = publicIP{}, ""
		p.mutex.Unlock()
		return publicIP{}, nil
	}
	hostname, _ := status["hostname"].(string)
	serverIP, _ := status["ip"].(string)
	key := hostname + "|" + serverIP

	p.mutex.Lock()
	defer p.mutex.Unlock()
	if key == p.cacheKey && time.Since(p.fetchedAt) < publicIPMaxAge {
		return p.cached, nil
	}

	var info publicIP
	switch p.api {
	case "ipinfo":
		info, err = p.fetchIPInfo(ctx)
	default:
		info, err = p.fetchNordVPN(ctx)
	}
	if err != nil {
		return publicIP{}, err
	}
	p.cached, p.cacheKey, p.fetchedAt = info, key, time.Now()
	return info, nil
}

func (p *publicIPFetcher) getJSON(ctx context.Context, url string, header map[string]string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	for key, value := range header {
		request.Header.Set(key, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, response.Status)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func (p *publicIPFetcher) fetchNordVPN(ctx context.Context) (publicIP, error) {
	var data struct {
		IP        string  `json:"ip"`
		Country   string  `json:"country"`
		City      string  `json:"city"`
		ISP       string  `json:"isp"`
		ISPASN    int     `json:"isp_asn"`
		Protected bool    `json:"protected"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		StateCode string  `json:"state_code"`
		ZipCode   string  `json:"zip_code"`
	}
	err := p.getJSON(ctx, "https://api.nordvpn.com/v1/helpers/ips/insights", nil, &data)
	if err != nil {
		return publicIP{}, err
	}
	info := publicIP{
		IP:         data.IP,
		Region:     data.StateCode,
		Country:    data.Country,
		City:       data.City,
		PostalCode: data.ZipCode,
		Protected:  &data.Protected,
	}
	if data.Latitude != 0 || data.Longitude != 0 {
		info.Location = fmt.Sprintf("%.4f,%.4f", data.Latitude, data.Longitude)
	}
	switch {
	case data.ISPASN != 0 && data.ISP != "":
		info.Organization = fmt.Sprintf("AS%d %s", data.ISPASN, data.ISP)
	default:
		info.Organization = data.ISP
	}
	return info, nil
}

func (p *publicIPFetcher) fetchIPInfo(ctx context.Context) (publicIP, error) {
	var data struct {
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
		City     string `json:"city"`
		Region   string `json:"region"`
		Country  string `json:"country"`
		Loc      string `json:"loc"`
		Org      string `json:"org"`
		Postal   string `json:"postal"`
		Timezone string `json:"timezone"`
	}
	header := map[string]string{}
	if p.token != "" {
		header["Authorization"] = "Bearer " + p.token
	}
	if err := p.getJSON(ctx, "https://ipinfo.io/json", header, &data); err != nil {
		return publicIP{}, err
	}
	return publicIP{
		IP:           data.IP,
		Region:       data.Region,
		Country:      data.Country,
		City:         data.City,
		Hostname:     data.Hostname,
		Location:     data.Loc,
		Organization: data.Org,
		PostalCode:   data.Postal,
		Timezone:     data.Timezone,
	}, nil
}
