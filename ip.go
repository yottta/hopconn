package hopconn

import (
	"fmt"
	"io"
	"net"
	"net/http"
)

type IPProvider func() (string, error)

func DefaultPublicIP() (string, error) {
	c := http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	return GlobalIP(c)
}

func GlobalIP(client http.Client) (string, error) {
	url := "https://api.ipify.org?format=text"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(ip), nil
}

func LocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addrs {
		// check the address type and if it is not a loopback the display it
		if ip, ok := address.(*net.IPNet); ok && !ip.IP.IsLoopback() {
			if ip.IP.To4() != nil {
				return ip.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("could not figure out the IP of your machine")
}
