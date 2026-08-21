package main

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mcastGroup    = "239.2.3.1"
	mcastPort     = 6969
	controlIface  = "br0"
	coreAPI       = "https://127.0.0.1/api/data"
	eudStaleAfter = 60 * time.Second
)

type Config struct {
	GPSSource       string // receiver | manual | eud
	ManualLat       float64
	ManualLon       float64
	ManualAlt       float64
	EUDUID          string
	RefreshInterval int
}

var (
	cfg      Config
	cfgMu    sync.RWMutex
	hostname string

	eudMu      sync.Mutex
	eudLat     float64
	eudLon     float64
	eudAlt     float64
	eudUpdated time.Time
)

func loadConfig(path string) Config {
	c := Config{GPSSource: "receiver", RefreshInterval: 15}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch k {
		case "GPS_SOURCE":
			if v == "receiver" || v == "manual" || v == "eud" {
				c.GPSSource = v
			}
		case "MANUAL_LAT":
			c.ManualLat, _ = strconv.ParseFloat(v, 64)
		case "MANUAL_LON":
			c.ManualLon, _ = strconv.ParseFloat(v, 64)
		case "MANUAL_ALT":
			c.ManualAlt, _ = strconv.ParseFloat(v, 64)
		case "EUD_UID":
			c.EUDUID = v
		case "REFRESH_INTERVAL":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				c.RefreshInterval = n
			}
		}
	}
	return c
}

// --- Core status data (mirrors manet-ctrl's /api/data shape, fields we use) ---

type gpsInfo struct {
	Available bool   `json:"available"`
	Lat       string `json:"lat"`
	Lon       string `json:"lon"`
}

type node struct {
	ID       string  `json:"id"`
	Hostname string  `json:"hostname"`
	IP       string  `json:"ip"`
	TQ       *int    `json:"tq"`
	IsMe     bool    `json:"is_me"`
	GPS      gpsInfo `json:"gps"`
}

type edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	TQ     *int   `json:"tq"`
}

type statusData struct {
	Nodes []node `json:"nodes"`
	Edges []edge `json:"edges"`
}

var insecureClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

func fetchStatus() (*statusData, error) {
	resp, err := insecureClient.Get(coreAPI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var sd statusData
	if err := json.NewDecoder(resp.Body).Decode(&sd); err != nil {
		return nil, err
	}
	return &sd, nil
}

// --- EUD GPS takeover: listen on the same CoT multicast group cot-emitter
// already relays, and adopt one configured EUD's own self-position report
// as this node's position. ---

type cotPoint struct {
	Lat float64 `xml:"lat,attr"`
	Lon float64 `xml:"lon,attr"`
	Hae float64 `xml:"hae,attr"`
}

type cotEvent struct {
	XMLName xml.Name `xml:"event"`
	UID     string   `xml:"uid,attr"`
	Type    string   `xml:"type,attr"`
	Point   cotPoint `xml:"point"`
}

func eudListenLoop() {
	group, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", mcastGroup, mcastPort))
	if err != nil {
		log.Printf("eud-listen: resolve failed: %v", err)
		return
	}
	var conn *net.UDPConn
	for {
		iface, ierr := net.InterfaceByName(controlIface)
		if ierr == nil {
			conn, err = net.ListenMulticastUDP("udp4", iface, group)
			if err == nil {
				break
			}
		} else {
			err = ierr
		}
		log.Printf("eud-listen: cannot join %s on %s (%v), retrying in 15s", mcastGroup, controlIface, err)
		time.Sleep(15 * time.Second)
	}
	defer conn.Close()
	log.Printf("eud-listen: joined %s:%d via %s", mcastGroup, mcastPort, controlIface)

	buf := make([]byte, 8192)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		cfgMu.RLock()
		targetUID := cfg.EUDUID
		active := cfg.GPSSource == "eud"
		cfgMu.RUnlock()
		if !active || targetUID == "" {
			continue
		}

		var ev cotEvent
		if xml.Unmarshal(buf[:n], &ev) != nil {
			continue
		}
		if ev.UID != targetUID {
			continue
		}
		if ev.Point.Lat == 0 && ev.Point.Lon == 0 {
			continue
		}

		eudMu.Lock()
		eudLat, eudLon, eudAlt = ev.Point.Lat, ev.Point.Lon, ev.Point.Hae
		eudUpdated = time.Now()
		eudMu.Unlock()
	}
}

// --- Self-position resolution per configured source ---

// resolveSelfGPS resolves this node's own position under the configured
// source. sd is an already-fetched core snapshot to reuse for the "receiver"
// case (pass nil to have it fetch one itself, e.g. from handleConfig where
// no snapshot exists yet) — callers that already hold one from fetchStatus()
// should pass it through rather than triggering a second /api/data round trip.
func resolveSelfGPS(sd *statusData) (lat, lon float64, ok bool, statusMsg string) {
	cfgMu.RLock()
	c := cfg
	cfgMu.RUnlock()

	switch c.GPSSource {
	case "manual":
		if c.ManualLat == 0 && c.ManualLon == 0 {
			return 0, 0, false, "no manual position set"
		}
		return c.ManualLat, c.ManualLon, true, "manual"
	case "eud":
		eudMu.Lock()
		lat, lon, updated := eudLat, eudLon, eudUpdated
		eudMu.Unlock()
		if updated.IsZero() {
			return 0, 0, false, "waiting for EUD " + c.EUDUID
		}
		if time.Since(updated) > eudStaleAfter {
			return 0, 0, false, fmt.Sprintf("EUD position stale (%.0fs old)", time.Since(updated).Seconds())
		}
		return lat, lon, true, "eud"
	default: // receiver
		if sd == nil {
			var err error
			sd, err = fetchStatus()
			if err != nil {
				return 0, 0, false, "core unreachable: " + err.Error()
			}
		}
		for _, n := range sd.Nodes {
			if n.IsMe && n.GPS.Available {
				lat, _ := strconv.ParseFloat(n.GPS.Lat, 64)
				lon, _ := strconv.ParseFloat(n.GPS.Lon, 64)
				return lat, lon, true, "receiver"
			}
		}
		return 0, 0, false, "no GPS fix"
	}
}

// --- HTTP handlers ---

func handleConfig(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	c := cfg
	cfgMu.RUnlock()

	lat, lon, ok, statusMsg := resolveSelfGPS(nil)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"gps_source":       c.GPSSource,
		"manual_lat":       c.ManualLat,
		"manual_lon":       c.ManualLon,
		"manual_alt":       c.ManualAlt,
		"eud_uid":          c.EUDUID,
		"refresh_interval": c.RefreshInterval,
		"resolved":         ok,
		"resolved_lat":     lat,
		"resolved_lon":     lon,
		"status":           statusMsg,
	})
}

func kmlColorForTQ(tq *int) string {
	switch {
	case tq == nil:
		return "linkUnknown"
	case *tq >= 200:
		return "linkGood"
	case *tq >= 100:
		return "linkFair"
	default:
		return "linkPoor"
	}
}

// handleTopologyKML renders node positions and link-quality edges as KML.
//
// Everything here comes from one fetchStatus() call, so a node's placemark
// and every edge touching it are drawn from the same registry snapshot —
// a peer's GPS only refreshes on its own ~15s registry gossip tick, so
// treating position as "the position that produced this edge's TQ reading"
// keeps a link's drawn endpoint from drifting off its marker between KML
// refreshes, instead of pairing it with a separately time-stamped live feed.
func handleTopologyKML(w http.ResponseWriter, r *http.Request) {
	sd, err := fetchStatus()
	if err != nil {
		http.Error(w, "core unreachable: "+err.Error(), 502)
		return
	}

	type point struct{ lat, lon float64 }
	coords := make(map[string]point, len(sd.Nodes))

	selfLat, selfLon, selfOK, _ := resolveSelfGPS(sd)

	var placemarks strings.Builder
	for _, n := range sd.Nodes {
		var lat, lon float64
		var have bool
		if n.IsMe {
			lat, lon, have = selfLat, selfLon, selfOK
		} else if n.GPS.Available {
			lat, _ = strconv.ParseFloat(n.GPS.Lat, 64)
			lon, _ = strconv.ParseFloat(n.GPS.Lon, 64)
			have = true
		}
		if !have {
			continue
		}
		coords[n.ID] = point{lat, lon}

		name := n.Hostname
		if name == "" {
			name = n.ID
		}
		style := "manetNode"
		if n.IsMe {
			style = "manetNodeSelf"
		}
		tq := "-"
		if n.TQ != nil {
			tq = strconv.Itoa(*n.TQ)
		}
		fmt.Fprintf(&placemarks, `
    <Placemark>
      <name>%s</name>
      <styleUrl>#%s</styleUrl>
      <ExtendedData>
        <Data name="tq"><value>%s</value></Data>
        <Data name="ip"><value>%s</value></Data>
      </ExtendedData>
      <Point>
        <altitudeMode>clampToGround</altitudeMode>
        <coordinates>%f,%f,0</coordinates>
      </Point>
    </Placemark>`, xmlEscape(name), style, xmlEscape(tq), xmlEscape(n.IP), lon, lat)
	}

	var lines strings.Builder
	for _, e := range sd.Edges {
		src, ok := coords[e.Source]
		if !ok {
			continue
		}
		dst, ok := coords[e.Target]
		if !ok {
			continue
		}
		style := kmlColorForTQ(e.TQ)
		tq := "-"
		if e.TQ != nil {
			tq = strconv.Itoa(*e.TQ)
		}
		fmt.Fprintf(&lines, `
    <Placemark>
      <name>%s to %s (TQ %s)</name>
      <styleUrl>#%s</styleUrl>
      <LineString>
        <altitudeMode>clampToGround</altitudeMode>
        <tessellate>1</tessellate>
        <coordinates>%f,%f,0 %f,%f,0</coordinates>
      </LineString>
    </Placemark>`, xmlEscape(e.Source), xmlEscape(e.Target), xmlEscape(tq), style,
			src.lon, src.lat, dst.lon, dst.lat)
	}

	kml := `<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2">
  <Document>
    <name>MANET Mesh Topology</name>
    <Style id="manetNode">
      <IconStyle><color>ffff8000</color><scale>1.1</scale></IconStyle>
      <LabelStyle><scale>0.8</scale></LabelStyle>
    </Style>
    <Style id="manetNodeSelf">
      <IconStyle><color>ff00a5ff</color><scale>1.3</scale></IconStyle>
      <LabelStyle><scale>0.9</scale></LabelStyle>
    </Style>
    <Style id="linkGood"><LineStyle><color>ff00ff00</color><width>3</width></LineStyle></Style>
    <Style id="linkFair"><LineStyle><color>ff00ffff</color><width>3</width></LineStyle></Style>
    <Style id="linkPoor"><LineStyle><color>ff0000ff</color><width>3</width></LineStyle></Style>
    <Style id="linkUnknown"><LineStyle><color>ff888888</color><width>2</width></LineStyle></Style>` +
		placemarks.String() + lines.String() + `
  </Document>
</kml>`

	w.Header().Set("Content-Type", "application/vnd.google-earth.kml+xml")
	w.Write([]byte(kml))
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func xmlEscape(s string) string {
	return xmlEscaper.Replace(s)
}

// handleATAKPackage returns a Mission Package containing a NetworkLink seed
// that points ATAK at handleTopologyKML for ongoing refresh. The external
// host:port isn't visible on this side of manet-ctrl's reverse proxy (it
// rewrites the Host header to the backend's own loopback address), so the
// browser — which does know the address it used — supplies it via ?host=.
func handleATAKPackage(w http.ResponseWriter, r *http.Request) {
	// Core's applet proxy (proxyToBackend in manet-ctrl) strips the query
	// string before forwarding, so ?host= never arrives here — a header
	// survives that hop untouched, so the browser sends it that way instead.
	host := r.Header.Get("X-Overlay-Host")
	if host == "" {
		host = r.URL.Query().Get("host")
	}
	if host == "" {
		host = r.Host
	}

	cfgMu.RLock()
	interval := cfg.RefreshInterval
	cfgMu.RUnlock()

	seedKML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2">
  <Document>
    <name>MANET Mesh Topology</name>
    <NetworkLink>
      <name>MANET Mesh Topology</name>
      <Link>
        <href>https://%s/api/applets/atak-overlay/proxy/topology.kml</href>
        <refreshMode>onInterval</refreshMode>
        <refreshInterval>%d</refreshInterval>
      </Link>
    </NetworkLink>
  </Document>
</kml>`, host, interval)

	uid := "manet-atak-overlay-" + hostname
	manifest := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MissionPackageManifest version="2">
    <Configuration>
        <Parameter name="uid" value="%s"/>
        <Parameter name="name" value="MANET Mesh Topology Overlay"/>
    </Configuration>
    <Contents>
        <Content ignore="false" zipEntry="overlays/manet-topology.kml">
            <Parameter name="name" value="MANET Mesh Topology Overlay"/>
        </Content>
    </Contents>
</MissionPackageManifest>`, uid)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="manet-topology-overlay.zip"`)
	writeZip(w, map[string]string{
		"MANIFEST/manifest.xml":       manifest,
		"overlays/manet-topology.kml": seedKML,
	})
}

func writeZip(w http.ResponseWriter, files map[string]string) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			continue
		}
		f.Write([]byte(content))
	}
	zw.Close()
	w.Write(buf.Bytes())
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func main() {
	port := flag.Int("port", 9821, "HTTP port")
	configFile := flag.String("config", "/etc/atak-overlay.conf", "config file")
	flag.Parse()

	cfg = loadConfig(*configFile)
	hostname, _ = os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	go func() {
		for {
			cfgMu.RLock()
			need := cfg.GPSSource == "eud"
			cfgMu.RUnlock()
			if need {
				eudListenLoop()
			}
			time.Sleep(5 * time.Second)
		}
	}()

	go func() {
		for range time.Tick(10 * time.Second) {
			cfgMu.Lock()
			cfg = loadConfig(*configFile)
			cfgMu.Unlock()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/config", handleConfig)
	mux.HandleFunc("/topology.kml", handleTopologyKML)
	mux.HandleFunc("/atak-package.zip", handleATAKPackage)
	mux.HandleFunc("/health", handleHealth)

	listen := fmt.Sprintf("0.0.0.0:%d", *port)
	log.Printf("atak-overlay: host=%s listen=%s gps_source=%s", hostname, listen, cfg.GPSSource)
	log.Fatal(http.ListenAndServe(listen, mux))
}
