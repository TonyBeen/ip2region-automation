package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expected   string
	}{
		{
			name: "X-Forwarded-For single",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.195",
			},
			remoteAddr: "192.168.1.1:12345",
			expected:   "203.0.113.195",
		},
		{
			name: "X-Forwarded-For multiple",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.195, 70.41.3.18, 150.172.238.178",
			},
			remoteAddr: "192.168.1.1:12345",
			expected:   "203.0.113.195",
		},
		{
			name: "X-Real-IP",
			headers: map[string]string{
				"X-Real-IP": "198.51.100.1",
			},
			remoteAddr: "127.0.0.1:54321",
			expected:   "198.51.100.1",
		},
		{
			name:       "RemoteAddr fallback",
			headers:    map[string]string{},
			remoteAddr: "192.0.2.1:8080",
			expected:   "192.0.2.1",
		},
		{
			name:       "IPv6 RemoteAddr",
			headers:    map[string]string{},
			remoteAddr: "[2001:db8::1]:9999",
			expected:   "2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			req.RemoteAddr = tt.remoteAddr
			got := extractClientIP(req)
			if got != tt.expected {
				t.Errorf("extractClientIP() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestParseRegion(t *testing.T) {
	// 1. 7-field format (fa1seut0pia / GeoCN): sample query test case
	sampleRegion := "亚洲|中国|江苏省|南京市|浦口区|电信|"
	sampleIP := "203.0.113.189"
	sampleInfo := parseRegion(sampleIP, sampleRegion)
	if sampleInfo.Continent != "亚洲" || sampleInfo.Country != "中国" || sampleInfo.Province != "江苏省" ||
		sampleInfo.City != "南京市" || sampleInfo.District != "浦口区" || sampleInfo.ISP != "电信" ||
		sampleInfo.NetType != "" || sampleInfo.Code != "CN" {
		t.Errorf("unexpected 7-field sample result: %+v", sampleInfo)
	}

	// 2. 7-field foreign IP: Google 8.8.8.8
	googleRegion := "北美洲|美国||||谷歌云|AS15169"
	googleInfo := parseRegion("8.8.8.8", googleRegion)
	if googleInfo.Continent != "北美洲" || googleInfo.Country != "美国" || googleInfo.Province != "" ||
		googleInfo.City != "" || googleInfo.District != "" || googleInfo.ISP != "谷歌云" ||
		googleInfo.NetType != "AS15169" || googleInfo.Code != "US" {
		t.Errorf("unexpected 7-field foreign result: %+v", googleInfo)
	}

	// 3. 7-field Anycast: 1.1.1.1
	cfRegion := "|||||Cloudflare|AS13335"
	cfInfo := parseRegion("1.1.1.1", cfRegion)
	if cfInfo.Continent != "" || cfInfo.Country != "" || cfInfo.Province != "" ||
		cfInfo.City != "" || cfInfo.District != "" || cfInfo.ISP != "Cloudflare" ||
		cfInfo.NetType != "AS13335" || cfInfo.Code != "" {
		t.Errorf("unexpected 7-field anycast result: %+v", cfInfo)
	}

	// 4. 5-field with ISO code
	region := "中国|广东省|深圳市|电信|CN"
	info := parseRegion("14.215.177.38", region)
	if info.Country != "中国" || info.Province != "广东省" || info.City != "深圳市" || info.ISP != "电信" || info.Code != "CN" {
		t.Errorf("unexpected 5-field result: %+v", info)
	}

	// 5. 5-field with Area and cleanZero
	regionZero := "美国|0|0|Cloudflare|US"
	infoZero := parseRegion("1.1.1.1", regionZero)
	if infoZero.Country != "美国" || infoZero.Province != "" || infoZero.City != "" || infoZero.ISP != "Cloudflare" || infoZero.Code != "US" {
		t.Errorf("cleanZero failed: %+v", infoZero)
	}

	// 6. 5-field without code (country lookup)
	regionArea := "中国|华南|广东省|广州市|联通"
	infoArea := parseRegion("211.95.193.97", regionArea)
	if infoArea.Country != "中国" || infoArea.Province != "广东省" || infoArea.City != "广州市" || infoArea.ISP != "联通" || infoArea.Code != "CN" {
		t.Errorf("area format failed: %+v", infoArea)
	}

	// 7. Internal IP
	internalRegion := "内网IP|内网IP|内网IP|私有网络C类||私有网络C类|"
	internalInfo := parseRegion("192.168.1.1", internalRegion)
	if internalInfo.Country != "内网IP" || internalInfo.City != "私有网络C类" || internalInfo.Code != "" {
		t.Errorf("internal IP failed: %+v", internalInfo)
	}
}

func TestPrefixAndHealthz(t *testing.T) {
	app := &App{
		prefix: "/ifconfig",
	}

	// Test 1: access /ifconfig/healthz
	req := httptest.NewRequest(http.MethodGet, "/ifconfig/healthz", nil)
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var res map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&res); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if res["status"] != "waiting_for_data" {
		t.Errorf("expected status waiting_for_data, got %v", res["status"])
	}

	// Test 2: access /healthz with X-Forwarded-Prefix: /ifconfig
	appNoPrefix := &App{prefix: ""}
	req2 := httptest.NewRequest(http.MethodGet, "/ifconfig/healthz", nil)
	req2.Header.Set("X-Forwarded-Prefix", "/ifconfig")
	rr2 := httptest.NewRecorder()
	appNoPrefix.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for forwarded prefix, got %d", rr2.Code)
	}
}

func TestFullQueryIntegration(t *testing.T) {
	repoDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	v4Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v4.xdb")
	v6Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v6.xdb")
	if _, err := os.Stat(v4Src); os.IsNotExist(err) {
		t.Skip("sample xdb files not found, skipping full integration test")
	}

	tmpDir := t.TempDir()
	currentDir := filepath.Join(tmpDir, "current")
	if err := os.MkdirAll(currentDir, 0755); err != nil {
		t.Fatal(err)
	}

	// symlink files into current
	if err := os.Symlink(v4Src, filepath.Join(currentDir, "ip2region_v4.xdb")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v6Src, filepath.Join(currentDir, "ip2region_v6.xdb")); err != nil {
		t.Fatal(err)
	}

	app := &App{
		dataDir:     tmpDir,
		prefix:      "/ifconfig",
		cachePolicy: "vindex",
		searchers:   2,
	}

	if err := app.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Test Query IPv4: /ifconfig/1.1.1.1 (JSON)
	req := httptest.NewRequest(http.MethodGet, "/ifconfig/1.1.1.1", nil)
	req.Header.Set("Accept", "application/json")
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var info IPInfo
	if err := json.NewDecoder(rr.Body).Decode(&info); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if info.IP != "1.1.1.1" || info.Region == "" {
		t.Errorf("unexpected IPInfo: %+v", info)
	}
	t.Logf("1.1.1.1 -> Region: %s, Country: %s, ISP: %s", info.Region, info.Country, info.ISP)

	// Test Query IPv6: /ifconfig/240e:3b7:3272:d8d0::1 (JSON)
	reqV6 := httptest.NewRequest(http.MethodGet, "/ifconfig/240e:3b7:3272:d8d0::1", nil)
	reqV6.Header.Set("Accept", "application/json")
	rrV6 := httptest.NewRecorder()
	app.ServeHTTP(rrV6, reqV6)

	if rrV6.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rrV6.Code, rrV6.Body.String())
	}
	var infoV6 IPInfo
	if err := json.NewDecoder(rrV6.Body).Decode(&infoV6); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if infoV6.IP != "240e:3b7:3272:d8d0::1" || infoV6.Region == "" {
		t.Errorf("unexpected IPv6 IPInfo: %+v", infoV6)
	}
	t.Logf("IPv6 -> Region: %s, Country: %s", infoV6.Region, infoV6.Country)

	// Test Client IP auto-detection: /ifconfig (default JSON output with caller IP)
	reqCurl := httptest.NewRequest(http.MethodGet, "/ifconfig", nil)
	reqCurl.Header.Set("User-Agent", "curl/8.5.0")
	reqCurl.Header.Set("X-Real-IP", "1.1.1.1")
	rrCurl := httptest.NewRecorder()
	app.ServeHTTP(rrCurl, reqCurl)

	if rrCurl.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrCurl.Code)
	}
	var clientInfo IPInfo
	if err := json.NewDecoder(rrCurl.Body).Decode(&clientInfo); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if clientInfo.IP != "1.1.1.1" || clientInfo.Region == "" {
		t.Errorf("expected client IP 1.1.1.1 and region, got %+v", clientInfo)
	}

	// Test Plain IP format: /ifconfig?format=ip
	reqPlainIP := httptest.NewRequest(http.MethodGet, "/ifconfig?format=ip", nil)
	reqPlainIP.Header.Set("X-Real-IP", "1.1.1.1")
	rrPlainIP := httptest.NewRecorder()
	app.ServeHTTP(rrPlainIP, reqPlainIP)
	if rrPlainIP.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrPlainIP.Code)
	}
	if strings.TrimSpace(rrPlainIP.Body.String()) != "1.1.1.1" {
		t.Errorf("expected plain IP 1.1.1.1, got %q", rrPlainIP.Body.String())
	}

	// Test Plain text query of region: /ifconfig/1.1.1.1?format=text
	reqText := httptest.NewRequest(http.MethodGet, "/ifconfig/1.1.1.1?format=text", nil)
	rrText := httptest.NewRecorder()
	app.ServeHTTP(rrText, reqText)
	if rrText.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrText.Code)
	}
	if strings.TrimSpace(rrText.Body.String()) != info.Region {
		t.Errorf("expected region %q, got %q", info.Region, rrText.Body.String())
	}
}

func BenchmarkConcurrentSearch(b *testing.B) {
	repoDir, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	v4Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v4.xdb")
	v6Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v6.xdb")
	if _, err := os.Stat(v4Src); os.IsNotExist(err) {
		b.Skip("sample xdb files not found")
	}

	tmpDir := b.TempDir()
	currentDir := filepath.Join(tmpDir, "current")
	os.MkdirAll(currentDir, 0755)
	os.Symlink(v4Src, filepath.Join(currentDir, "ip2region_v4.xdb"))
	os.Symlink(v6Src, filepath.Join(currentDir, "ip2region_v6.xdb"))

	app := &App{
		dataDir:     tmpDir,
		prefix:      "/ifconfig",
		cachePolicy: "vindex",
		searchers:   100,
	}
	if err := app.Reload(); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := app.Search("1.1.1.1")
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkConcurrentSearchContent(b *testing.B) {
	repoDir, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	v4Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v4.xdb")
	v6Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v6.xdb")
	if _, err := os.Stat(v4Src); os.IsNotExist(err) {
		b.Skip("sample xdb files not found")
	}

	tmpDir := b.TempDir()
	currentDir := filepath.Join(tmpDir, "current")
	os.MkdirAll(currentDir, 0755)
	os.Symlink(v4Src, filepath.Join(currentDir, "ip2region_v4.xdb"))
	os.Symlink(v6Src, filepath.Join(currentDir, "ip2region_v6.xdb"))

	app := &App{
		dataDir:     tmpDir,
		prefix:      "/ifconfig",
		cachePolicy: "content",
		searchers:   50,
	}
	if err := app.Reload(); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := app.Search("1.1.1.1")
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func TestBatchQuery(t *testing.T) {
	repoDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	v4Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v4.xdb")
	v6Src := filepath.Join(repoDir, "submodules/ip2region/data/ip2region_v6.xdb")
	if _, err := os.Stat(v4Src); os.IsNotExist(err) {
		t.Skip("sample xdb files not found")
	}

	tmpDir := t.TempDir()
	currentDir := filepath.Join(tmpDir, "current")
	os.MkdirAll(currentDir, 0755)
	os.Symlink(v4Src, filepath.Join(currentDir, "ip2region_v4.xdb"))
	os.Symlink(v6Src, filepath.Join(currentDir, "ip2region_v6.xdb"))

	app := &App{
		dataDir:     tmpDir,
		prefix:      "/ifconfig",
		cachePolicy: "vindex",
		searchers:   10,
	}
	if err := app.Reload(); err != nil {
		t.Fatal(err)
	}

	// 1. Test GET /ifconfig?ips=1.1.1.1,240e:3b7:3272:d8d0::1,invalid.ip
	reqGET := httptest.NewRequest(http.MethodGet, "/ifconfig?ips=1.1.1.1,240e:3b7:3272:d8d0::1,invalid.ip", nil)
	rrGET := httptest.NewRecorder()
	app.ServeHTTP(rrGET, reqGET)

	if rrGET.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rrGET.Code, rrGET.Body.String())
	}
	var resGET []BatchIPResult
	if err := json.NewDecoder(rrGET.Body).Decode(&resGET); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(resGET) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resGET))
	}
	if resGET[0].IP != "1.1.1.1" || resGET[0].Country != "Australia" {
		t.Errorf("unexpected resGET[0]: %+v", resGET[0])
	}
	if resGET[1].IP != "240e:3b7:3272:d8d0::1" || resGET[1].Country != "中国" {
		t.Errorf("unexpected resGET[1]: %+v", resGET[1])
	}
	if resGET[2].Error == "" {
		t.Errorf("expected error for invalid.ip, got none")
	}

	// 2. Test POST /ifconfig/batch with JSON array body
	reqPOST := httptest.NewRequest(http.MethodPost, "/ifconfig/batch", strings.NewReader(`["1.1.1.1", "240e:3b7:3272:d8d0::1"]`))
	rrPOST := httptest.NewRecorder()
	app.ServeHTTP(rrPOST, reqPOST)

	if rrPOST.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rrPOST.Code, rrPOST.Body.String())
	}
	var resPOST []BatchIPResult
	if err := json.NewDecoder(rrPOST.Body).Decode(&resPOST); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(resPOST) != 2 || resPOST[0].Country != "Australia" || resPOST[1].Country != "中国" {
		t.Errorf("unexpected resPOST: %+v", resPOST)
	}
}


