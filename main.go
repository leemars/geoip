// GeoIP generator
//
// Before running this file, the GeoIP database must be downloaded and present.
// To download GeoIP database: https://dev.maxmind.com/geoip/geoip2/geolite2/
// Inside you will find block files for IPv4 and IPv6 and country code mapping.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/v2fly/v2ray-core/v4/app/router"
	"github.com/v2fly/v2ray-core/v4/infra/conf/rule"
	"google.golang.org/protobuf/proto"
)

var (
	countryCodeFile = flag.String("country", "GeoLite2-Country-Locations-en.csv", "Path to the country code file")
	ipv4File        = flag.String("ipv4", "GeoLite2-Country-Blocks-IPv4.csv", "Path to the IPv4 block file")
	ipv6File        = flag.String("ipv6", "GeoLite2-Country-Blocks-IPv6.csv", "Path to the IPv6 block file")
	ipv4CNURI       = flag.String("ipv4CN", "", "URI of CN IPv4 CIDR file")
	inputDir        = flag.String("inputdir", "./data", "Path to the input directory")
	outputName      = flag.String("outputname", "geoip.dat", "Name of the generated file")
	outputDir       = flag.String("outputdir", "./", "Path to the output directory")
)

func getCidrPerFile(dataDirMap map[string][]*router.CIDR) error {
	walkErr := filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		filename := strings.ToUpper(filepath.Base(path))
		cidrContainer := make([]*router.CIDR, 0)
		if err = readFileLineByLine(path, &cidrContainer); err != nil {
			return err
		}
		dataDirMap[filename] = cidrContainer

		return nil
	})

	if walkErr != nil {
		return walkErr
	}
	return nil
}

func readFileLineByLine(path string, container *[]*router.CIDR) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		dummyLine := strings.TrimSpace(scanner.Text())
		if len(dummyLine) == 0 {
			continue
		}
		if i := strings.Index(dummyLine, "#"); i > 0 {
			dummyLine = strings.TrimSpace(dummyLine[:i])
		}
		cidr, err := rule.ParseIP(dummyLine)
		if err != nil {
			return err
		}
		*container = append(*container, cidr)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func getCountryCodeMap() (map[string]string, error) {
	countryCodeReader, err := os.Open(*countryCodeFile)
	if err != nil {
		return nil, err
	}
	defer countryCodeReader.Close()

	m := make(map[string]string)
	reader := csv.NewReader(countryCodeReader)
	lines, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	for _, line := range lines[1:] {
		id := line[0]
		countryCode := line[4]
		if len(countryCode) == 0 {
			continue
		}
		m[id] = strings.ToUpper(countryCode)
	}
	return m, nil
}

func getCidrPerCountry(file string, m map[string]string, list map[string][]*router.CIDR) error {
	fileReader, err := os.Open(file)
	if err != nil {
		return err
	}
	defer fileReader.Close()

	reader := csv.NewReader(fileReader)
	lines, err := reader.ReadAll()
	if err != nil {
		return err
	}
	for _, line := range lines[1:] {
		cidrStr := line[0]
		countryID := line[1]
		if countryCode, found := m[countryID]; found {
			cidr, err := rule.ParseIP(cidrStr)
			if err != nil {
				return err
			}
			cidrs := append(list[countryCode], cidr)
			list[countryCode] = cidrs
		}
	}
	return nil
}

func getCNIPv4Cidr(path string) (cnIPv4CidrList []string, err error) {
	isURL := strings.HasPrefix(path, "http")
	var body []byte
	if !isURL {
		fmt.Println("Reading local file:", path)
		body, err = ioutil.ReadFile(path)
		if err != nil {
			return nil, err
		}
	} else {
		fmt.Println("Fetching content of URL:", path)
		req, err := http.NewRequest("GET", path, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		data, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer data.Close()

		body, err = ioutil.ReadAll(data)
		if err != nil {
			return nil, err
		}
	}

	reg := regexp.MustCompile(`(\d+\.){3}\d+\/\d+`)
	matchedCIDRList := reg.FindAllStringSubmatch(string(body), -1)

	if len(matchedCIDRList) > 0 {
		for _, cidr := range matchedCIDRList {
			cnIPv4CidrList = append(cnIPv4CidrList, cidr[0])
		}
		fmt.Println("The length of cnIPv4CIDRList is", len(cnIPv4CidrList))
		return cnIPv4CidrList, nil
	}
	err = errors.New("no matching IP CIDR addresses")
	return nil, err
}

func changeCNIPv4Cidr(url string, m map[string]string, list map[string][]*router.CIDR) error {
	// delete "CN" Key in list
	delete(list, "CN")
	fmt.Println("Successfully deleted CN IPv4 CIDR")
	fmt.Println(list["CN"])

	cnIPv4CIDRList, err := getCNIPv4Cidr(url)
	if err != nil {
		return err
	}

	for _, cnIPv4CIDR := range cnIPv4CIDRList {
		fmt.Println("Processing CN IPv4 CIDR:", cnIPv4CIDR)
		cnIPv4CIDR, err := rule.ParseIP(strings.TrimSpace(cnIPv4CIDR))
		if err != nil {
			return err
		}
		cidrs := append(list["CN"], cnIPv4CIDR)
		list["CN"] = cidrs
	}

	return nil
}

func main() {
	flag.Parse()

	ccMap, err := getCountryCodeMap()
	if err != nil {
		fmt.Println("Error reading country code map:", err)
		os.Exit(1)
	}

	cidrList := make(map[string][]*router.CIDR)
	if *ipv4File != "" {
		if err := getCidrPerCountry(*ipv4File, ccMap, cidrList); err != nil {
			fmt.Println("Error loading IPv4 file:", err)
			os.Exit(1)
		}
	}
	if *ipv4CNURI != "" {
		if err := changeCNIPv4Cidr(*ipv4CNURI, ccMap, cidrList); err != nil {
			fmt.Println("Error loading ipv4CNURI data:", err)
			os.Exit(1)
		}
	}
	if *ipv6File != "" {
		if err := getCidrPerCountry(*ipv6File, ccMap, cidrList); err != nil {
			fmt.Println("Error loading IPv6 file:", err)
			os.Exit(1)
		}
	}
	if err := getCidrPerFile(cidrList); err != nil {
		fmt.Println("Error looping data directory:", err)
		os.Exit(1)
	}

	geoIPList := new(router.GeoIPList)
	for cc, cidr := range cidrList {
		geoIPList.Entry = append(geoIPList.Entry, &router.GeoIP{
			CountryCode: cc,
			Cidr:        cidr,
		})
	}

	geoIPBytes, err := proto.Marshal(geoIPList)
	if err != nil {
		fmt.Println("Error marshalling geoip list:", err)
		os.Exit(1)
	}

	// Create output directory if not exist
	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(*outputDir, 0755); mkErr != nil {
			fmt.Println("Failed: ", mkErr)
			os.Exit(1)
		}
	}

	if err := ioutil.WriteFile(filepath.Join(*outputDir, *outputName), geoIPBytes, 0644); err != nil {
		fmt.Println("Error writing geoip to file:", err)
		os.Exit(1)
	} else {
		fmt.Println(*outputName, "has been generated successfully.")
	}
}
