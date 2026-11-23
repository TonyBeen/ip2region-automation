package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lionsoul2014/ip2region/binding/golang/service"
)

// Manifest represents the metadata saved by updater.py
type Manifest struct {
	Version string                 `json:"version"`
	Source  string                 `json:"source"`
	Assets  map[string]interface{} `json:"assets"`
}

// IPInfo holds structured query result
type IPInfo struct {
	IP        string `json:"ip"`
	Continent string `json:"continent"`
	Country   string `json:"country"`
	Province  string `json:"province"`
	City      string `json:"city"`
	District  string `json:"district"`
	ISP       string `json:"isp"`
	NetType   string `json:"net_type"`
	Code      string `json:"code"`
	Region    string `json:"region"`
}

// BatchIPResult holds result for batch IP query
type BatchIPResult struct {
	IP        string `json:"ip"`
	Continent string `json:"continent"`
	Country   string `json:"country"`
	Province  string `json:"province"`
	City      string `json:"city"`
	District  string `json:"district"`
	ISP       string `json:"isp"`
	NetType   string `json:"net_type"`
	Code      string `json:"code"`
	Region    string `json:"region"`
	Error     string `json:"error,omitempty"`
}

// App manages the ip2region service state and atomic hot-reloading
type App struct {
	dataDir     string
	prefix      string
	cachePolicy string
	searchers   int
	reloadToken string

	mu        sync.RWMutex
	ip2r      *service.Ip2Region
	manifest  *Manifest
	loadedAt  time.Time
	v4Enabled bool
	v6Enabled bool
}

func parseEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func cleanZero(val string) string {
	if val == "0" {
		return ""
	}
	return val
}

var continents = map[string]bool{
	"亚洲": true, "欧洲": true, "北美洲": true, "南美洲": true, "非洲": true, "大洋洲": true, "南极洲": true,
	"Asia": true, "Europe": true, "North America": true, "South America": true, "Africa": true, "Oceania": true, "Antarctica": true,
	"内网IP": true,
}

func parseRegion(ip, region string) IPInfo {
	info := IPInfo{
		IP:     ip,
		Region: region,
	}
	parts := strings.Split(region, "|")

	// Check if this is the 6 or 7-field format (fa1seut0pia / GeoCN / GeoLite2 format:
	// 洲|国家|省份|城市|区县|ISP|网络类型)
	is7Field := len(parts) >= 6 && (len(parts) >= 7 || continents[parts[0]] || parts[0] == "")

	if is7Field {
		info.Continent = cleanZero(parts[0])
		info.Country = cleanZero(parts[1])
		info.Province = cleanZero(parts[2])
		info.City = cleanZero(parts[3])
		info.District = cleanZero(parts[4])
		info.ISP = cleanZero(parts[5])
		if len(parts) >= 7 {
			info.NetType = cleanZero(parts[6])
		}
		info.Code = lookupCountryCode(info.Country)
	} else if len(parts) == 5 {
		// Standard Lionsoul 5-field formats:
		// Format A: 国家|省份|城市|ISP|Code (e.g. 中国|广东省|深圳市|电信|CN or Australia|Queensland|Brisbane|0|AU)
		// Format B: 国家|区域|省份|城市|ISP (e.g. 中国|0|广东省|深圳市|电信)
		last := cleanZero(parts[4])
		if isAlpha2(last) {
			info.Country = cleanZero(parts[0])
			info.Province = cleanZero(parts[1])
			info.City = cleanZero(parts[2])
			info.ISP = cleanZero(parts[3])
			info.Code = last
		} else {
			info.Country = cleanZero(parts[0])
			info.Province = cleanZero(parts[2])
			info.City = cleanZero(parts[3])
			info.ISP = cleanZero(parts[4])
			info.Code = lookupCountryCode(info.Country)
		}
	} else {
		// Fallback for arbitrary lengths
		if len(parts) >= 1 {
			info.Country = cleanZero(parts[0])
		}
		if len(parts) >= 2 {
			info.Province = cleanZero(parts[1])
		}
		if len(parts) >= 3 {
			info.City = cleanZero(parts[2])
		}
		if len(parts) >= 4 {
			info.ISP = cleanZero(parts[3])
		}
		if len(parts) >= 5 {
			info.Code = cleanZero(parts[4])
		}
		if info.Code == "" || !isAlpha2(info.Code) {
			if c := lookupCountryCode(info.Country); c != "" {
				info.Code = c
			}
		}
	}
	return info
}

func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

func (a *App) Reload() error {
	v4Path := filepath.Join(a.dataDir, "current", "ip2region_v4.xdb")
	v6Path := filepath.Join(a.dataDir, "current", "ip2region_v6.xdb")
	manifestPath := filepath.Join(a.dataDir, "current", "manifest.json")

	_, errV4 := os.Stat(v4Path)
	_, errV6 := os.Stat(v6Path)
	if os.IsNotExist(errV4) && os.IsNotExist(errV6) {
		return fmt.Errorf("neither v4 nor v6 database found in %s/current", a.dataDir)
	}

	cPolicy, err := service.CachePolicyFromName(a.cachePolicy)
	if err != nil {
		return fmt.Errorf("invalid cache policy %s: %w", a.cachePolicy, err)
	}

	var v4Cfg *service.Config
	if errV4 == nil {
		v4Cfg, err = service.NewV4Config(cPolicy, v4Path, a.searchers)
		if err != nil {
			return fmt.Errorf("failed to load v4 database (%s): %w", v4Path, err)
		}
	}

	var v6Cfg *service.Config
	if errV6 == nil {
		v6Cfg, err = service.NewV6Config(cPolicy, v6Path, a.searchers)
		if err != nil {
			return fmt.Errorf("failed to load v6 database (%s): %w", v6Path, err)
		}
	}

	newService, err := service.NewIp2Region(v4Cfg, v6Cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize Ip2Region service: %w", err)
	}

	var manifest *Manifest
	if data, err := os.ReadFile(manifestPath); err == nil {
		var m Manifest
		if err := json.Unmarshal(data, &m); err == nil {
			manifest = &m
		}
	}

	a.mu.Lock()
	old := a.ip2r
	a.ip2r = newService
	a.manifest = manifest
	a.loadedAt = time.Now()
	a.v4Enabled = (v4Cfg != nil)
	a.v6Enabled = (v6Cfg != nil)
	a.mu.Unlock()

	if old != nil {
		go func() {
			time.Sleep(5 * time.Second)
			old.Close()
		}()
	}

	log.Printf("[RELOAD] Database reloaded successfully (v4=%v, v6=%v, manifest=%+v)",
		a.v4Enabled, a.v6Enabled, manifest != nil)
	return nil
}

func (a *App) Search(ip string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.ip2r == nil {
		return "", errors.New("database not loaded yet")
	}
	return a.ip2r.Search(ip)
}

// extractClientIP retrieves the real client IP from headers or RemoteAddr
func extractClientIP(r *http.Request) string {
	// 1. X-Forwarded-For
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	// 2. X-Real-IP
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if xri != "" && net.ParseIP(xri) != nil {
		return xri
	}

	// 3. RemoteAddr (strip port)
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return ""
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Handle prefix:
	// Nginx can pass X-Forwarded-Prefix or user sets PATH_PREFIX.
	fwdPrefix := normalizePrefix(r.Header.Get("X-Forwarded-Prefix"))
	cfgPrefix := a.prefix

	// Check if path starts with cfgPrefix
	if cfgPrefix != "" && (path == cfgPrefix || strings.HasPrefix(path, cfgPrefix+"/")) {
		path = strings.TrimPrefix(path, cfgPrefix)
	} else if fwdPrefix != "" && (path == fwdPrefix || strings.HasPrefix(path, fwdPrefix+"/")) {
		path = strings.TrimPrefix(path, fwdPrefix)
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Route dispatch
	switch {
	case path == "/healthz":
		a.handleHealthz(w, r)
		return
	case path == "/reload":
		a.handleReload(w, r)
		return
	case path == "/batch":
		a.handleBatchQuery(w, r)
		return
	default:
		if r.URL.Query().Get("ips") != "" || (r.Method == http.MethodPost && (path == "/" || path == "/ip")) {
			a.handleBatchQuery(w, r)
			return
		}
		a.handleQuery(w, r, path)
		return
	}
}

func (a *App) handleBatchQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var ips []string

	// 1. Check Query parameter "ips" (e.g. ?ips=1.1.1.1,8.8.8.8)
	if qIPs := r.URL.Query().Get("ips"); qIPs != "" {
		for _, part := range strings.Split(qIPs, ",") {
			p := strings.TrimSpace(part)
			if p != "" {
				ips = append(ips, p)
			}
		}
	}

	// 2. If POST body exists, parse it
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max
		if err == nil && len(body) > 0 {
			var arr []string
			if err := json.Unmarshal(body, &arr); err == nil {
				ips = append(ips, arr...)
			} else {
				var obj struct {
					IPs []string `json:"ips"`
				}
				if err := json.Unmarshal(body, &obj); err == nil && len(obj.IPs) > 0 {
					ips = append(ips, obj.IPs...)
				} else {
					// Fallback to line-separated or comma-separated text
					lines := strings.FieldsFunc(string(body), func(c rune) bool {
						return c == '\n' || c == '\r' || c == ','
					})
					for _, l := range lines {
						p := strings.TrimSpace(l)
						if p != "" {
							ips = append(ips, p)
						}
					}
				}
			}
		}
	}

	// Cap at 1000 IPs max per batch
	if len(ips) > 1000 {
		ips = ips[:1000]
	}

	if len(ips) == 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "no IP addresses provided; provide via ?ips=1.1.1.1,8.8.8.8 or POST JSON [\"1.1.1.1\", ...]",
		})
		return
	}

	results := make([]BatchIPResult, len(ips))
	var wg sync.WaitGroup

	for i, ipStr := range ips {
		wg.Add(1)
		go func(idx int, rawIP string) {
			defer wg.Done()
			rawIP = strings.TrimSpace(rawIP)
			if net.ParseIP(rawIP) == nil {
				results[idx] = BatchIPResult{
					IP:    rawIP,
					Error: fmt.Sprintf("invalid ip address: %q", rawIP),
				}
				return
			}

			region, err := a.Search(rawIP)
			if err != nil {
				results[idx] = BatchIPResult{
					IP:    rawIP,
					Error: err.Error(),
				}
				return
			}

			info := parseRegion(rawIP, region)
			results[idx] = BatchIPResult{
				IP:        info.IP,
				Continent: info.Continent,
				Country:   info.Country,
				Province:  info.Province,
				City:      info.City,
				District:  info.District,
				ISP:       info.ISP,
				NetType:   info.NetType,
				Code:      info.Code,
				Region:    info.Region,
			}
		}(i, ipStr)
	}

	wg.Wait()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(results)
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	status := "ok"
	if a.ip2r == nil {
		status = "waiting_for_data"
	}

	resp := map[string]interface{}{
		"status":     status,
		"loaded":     a.ip2r != nil,
		"loaded_at":  a.loadedAt.Format(time.RFC3339),
		"v4_enabled": a.v4Enabled,
		"v6_enabled": a.v6Enabled,
		"prefix":     a.prefix,
	}
	if a.manifest != nil {
		resp["manifest"] = a.manifest
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

func (a *App) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "Method Not Allowed (use POST)", http.StatusMethodNotAllowed)
		return
	}

	if a.reloadToken != "" {
		token := r.Header.Get("X-Reload-Token")
		if token == "" {
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if token != a.reloadToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
	}

	if err := a.Reload(); err != nil {
		log.Printf("[RELOAD ERROR] %v", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    1,
			"message": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    0,
		"message": "reloaded successfully",
	})
}

func (a *App) handleQuery(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Determine target IP:
	// 1. Path segment: /<ip> or /ip/<ip>
	// 2. Query param: ?ip=<ip>
	// 3. Fallback: client IP
	var targetIP string
	trimmed := strings.Trim(path, "/")
	if strings.HasPrefix(trimmed, "ip/") {
		trimmed = strings.TrimPrefix(trimmed, "ip/")
	}

	if trimmed == "" || trimmed == "ip" {
		// Root or /ip
		if qIP := r.URL.Query().Get("ip"); qIP != "" {
			targetIP = strings.TrimSpace(qIP)
		} else {
			targetIP = extractClientIP(r)
		}
	} else {
		// Subpath e.g. /1.1.1.1 or /240e::1
		targetIP = trimmed
	}

	if net.ParseIP(targetIP) == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("invalid ip address: %q", targetIP),
		})
		return
	}

	region, err := a.Search(targetIP)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	formatParam := strings.ToLower(r.URL.Query().Get("format"))
	if formatParam == "ip" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, targetIP)
		return
	}
	if formatParam == "text" || formatParam == "raw" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, region)
		return
	}

	// Default: return pretty-printed JSON
	info := parseRegion(targetIP, region)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(info)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "28080"
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":" + port
	}

	dataDir := os.Getenv("IP2REGION_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/ip2region"
	}

	prefix := os.Getenv("PATH_PREFIX")
	if prefix == "" {
		prefix = "/ifconfig"
	}

	cachePolicy := os.Getenv("CACHE_POLICY")
	if cachePolicy == "" {
		cachePolicy = "vindex"
	}

	searchers := parseEnvInt("SEARCHERS", 50)
	reloadToken := os.Getenv("RELOAD_TOKEN")

	app := &App{
		dataDir:     dataDir,
		prefix:      normalizePrefix(prefix),
		cachePolicy: cachePolicy,
		searchers:   searchers,
		reloadToken: reloadToken,
	}

	// Try initial database load
	log.Printf("[START] Initializing ip2region Go server on %s with prefix=%q data_dir=%q",
		addr, app.prefix, app.dataDir)
	if err := app.Reload(); err != nil {
		log.Printf("[WARN] Initial reload failed: %v (service will listen and wait for updater)", err)
	}

	// SIGHUP signal listener for reload
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP)
	go func() {
		for range sigChan {
			log.Println("[SIGNAL] Received SIGHUP, reloading database...")
			if err := app.Reload(); err != nil {
				log.Printf("[SIGNAL ERROR] Failed to reload on SIGHUP: %v", err)
			}
		}
	}()

	server := &http.Server{
		Addr:         addr,
		Handler:      app,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] ListenAndServe error: %v", err)
		}
	}()
	log.Printf("[READY] Server started at http://%s%s", addr, app.prefix)

	<-stopChan
	log.Println("[STOP] Shutting down gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("[ERROR] Server shutdown error: %v", err)
	}

	app.mu.Lock()
	if app.ip2r != nil {
		app.ip2r.Close()
	}
	app.mu.Unlock()

	log.Println("[STOP] Service stopped.")
}
