package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fogleman/gg"
	"github.com/skip2/go-qrcode"
	"tinygo.org/x/bluetooth"
)

//go:embed dashboard.html
var dashboardHTML embed.FS

const (
	ConfigFile = "config.json"
	WriteUUID  = "0000ae01-0000-1000-8000-00805f9b34fb"
	PaperWidth = 384
)

type Config struct {
	MAC string `json:"mac"`
}

var (
	adapter   = bluetooth.DefaultAdapter
	appConfig = Config{MAC: "C4:76:44:3F:7F:E3"} // Default
)

func main() {
	loadConfig()

	// Initialize Bluetooth
	if err := adapter.Enable(); err != nil {
		log.Printf("Warning: bluetooth adapter failed: %v", err)
	}

	// Signal handling for clean exit
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down QR-Printer-Go...")
		os.Exit(0)
	}()

	// ROUTES
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/print", handlePrint)
	http.HandleFunc("/config", handleConfig)

	port := "2030"
	fmt.Printf("\n🚀 [QR-Printer-Go] Dashboard Ready!\n")
	fmt.Printf("📍 Dashboard & API: http://localhost:%s\n", port)
	fmt.Printf("🖨️  Active Printer: %s\n\n", appConfig.MAC)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

// HANDLERS
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := dashboardHTML.ReadFile("dashboard.html")
	w.Header().Set("Content-Type", "text/html")
	w.Write(data)
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var newCfg Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		appConfig.MAC = strings.ToUpper(newCfg.MAC)
		saveConfig()
		fmt.Printf("[Config] Updated MAC to: %s\n", appConfig.MAC)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(appConfig)
}

func handlePrint(w http.ResponseWriter, r *http.Request) {
	qrText := r.URL.Query().Get("qr")
	if qrText == "" {
		http.Error(w, `{"error": "Missing qr parameter"}`, http.StatusBadRequest)
		return
	}

	fmt.Printf("[Server] Print Request: %s\n", qrText)

	pixels, height, err := generateImage(qrText)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
		return
	}

	if err := printBLE(pixels, height); err != nil {
		fmt.Printf("[Error] %v\n", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}

// PRINTER LOGIC
func generateImage(text string) ([]byte, int, error) {
	const qrSize = 320
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil, 0, err
	}
	q.DisableBorder = true
	qrImg := q.Image(qrSize)

	const fontSize = 32
	dc := gg.NewContext(PaperWidth, 1000)
	// Try loading common fonts or fallback
	fontPaths := []string{
		"/usr/share/fonts/TTF/MesloLGS-NF-Regular.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
		"C:\\Windows\\Fonts\\arialbd.ttf",
	}
	fontLoaded := false
	for _, p := range fontPaths {
		if err := dc.LoadFontFace(p, fontSize); err == nil {
			fontLoaded = true
			break
		}
	}
	if !fontLoaded {
		fmt.Println("Warning: No system fonts found, text rendering might fail.")
	}

	lines := dc.WordWrap(text, PaperWidth-40)
	lineHeight := fontSize + 12
	textHeight := len(lines)*lineHeight + 60
	totalHeight := qrSize + textHeight + 40

	dest := gg.NewContext(PaperWidth, totalHeight)
	dest.SetColor(color.White)
	dest.Clear()

	if fontLoaded {
		for _, p := range fontPaths {
			if err := dest.LoadFontFace(p, fontSize); err == nil {
				break
			}
		}
	}

	dest.DrawImage(qrImg, (PaperWidth-qrSize)/2, 20)
	dest.SetColor(color.Black)
	textStartY := float64(qrSize + 60)
	for i, line := range lines {
		dest.DrawStringAnchored(line, PaperWidth/2, textStartY+float64(i*lineHeight), 0.5, 0.5)
	}

	img := dest.Image()
	pixels := make([]byte, PaperWidth*totalHeight)
	for y := 0; y < totalHeight; y++ {
		for x := 0; x < PaperWidth; x++ {
			c := img.At(x, y)
			r, g, b, _ := c.RGBA()
			if (r+g+b)/3 < 32768 {
				pixels[y*PaperWidth+x] = 1
			} else {
				pixels[y*PaperWidth+x] = 0
			}
		}
	}
	return pixels, totalHeight, nil
}

func printBLE(pixels []byte, height int) error {
	var targetAddr bluetooth.Address
	fmt.Printf("[Printer] Searching for %s...\n", appConfig.MAC)

	found := false
	err := adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		if strings.EqualFold(result.Address.String(), appConfig.MAC) {
			targetAddr = result.Address
			adapter.StopScan()
			found = true
		}
	})
	if err != nil {
		return err
	}

	if !found {
		return fmt.Errorf("printer not found. Check MAC address")
	}

	fmt.Printf("[Printer] Connecting...\n")
	device, err := adapter.Connect(targetAddr, bluetooth.ConnectionParams{})
	if err != nil {
		return err
	}
	defer device.Disconnect()

	services, err := device.DiscoverServices(nil)
	if err != nil {
		return err
	}

	var writeChar bluetooth.DeviceCharacteristic
	foundChar := false
	for _, s := range services {
		chars, _ := s.DiscoverCharacteristics(nil)
		for _, c := range chars {
			if strings.Contains(strings.ToLower(c.UUID().String()), "ae01") {
				writeChar = c
				foundChar = true
				break
			}
		}
		if foundChar {
			break
		}
	}

	if !foundChar {
		return fmt.Errorf("characteristic AE01 not found")
	}

	var job []byte
	job = append(job, makePacket(0xA4, []byte{0x35})...)
	job = append(job, makePacket(0xAF, []byte{0x1C, 0x25})...)
	job = append(job, makePacket(0xBE, []byte{0x00})...)
	job = append(job, makePacket(0xBD, []byte{0x06})...)

	for y := 0; y < height; y++ {
		line := pixels[y*PaperWidth : (y+1)*PaperWidth]
		rle := encodeRLE(line)
		if len(rle) <= PaperWidth/8 {
			job = append(job, makePacket(0xBF, rle)...)
		} else {
			job = append(job, makePacket(0xA2, packLine(line))...)
		}
	}
	job = append(job, makePacket(0xBD, []byte{0x0A})...)
	job = append(job, makePacket(0xA1, []byte{0x30, 0x00})...)

	fmt.Printf("[Printer] Sending %d bytes...\n", len(job))
	mtu := 123
	for i := 0; i < len(job); i += mtu {
		end := i + mtu
		if end > len(job) {
			end = len(job)
		}
		writeChar.WriteWithoutResponse(job[i:end])
		time.Sleep(6 * time.Millisecond)
	}

	fmt.Println("[Printer] Success")
	return nil
}

func makePacket(cmd byte, payload []byte) []byte {
	l := len(payload)
	header := []byte{0x51, 0x78, cmd, 0x00, byte(l & 0xFF), byte((l >> 8) & 0xFF)}
	crc := byte(0)
	for _, b := range payload {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return append(append(header, payload...), crc, 0xFF)
}

func encodeRLE(line []byte) []byte {
	if len(line) == 0 {
		return nil
	}
	var runs []byte
	prev, count, hasBlack := line[0], 0, false
	for _, pix := range line {
		if pix == 1 {
			hasBlack = true
		}
		if pix == prev {
			count++
		} else {
			for count > 127 {
				runs = append(runs, (prev<<7)|127)
				count -= 127
			}
			if count > 0 {
				runs = append(runs, (prev<<7)|byte(count))
			}
			prev, count = pix, 1
		}
	}
	if hasBlack || count > 0 {
		for count > 127 {
			runs = append(runs, (prev<<7)|127)
			count -= 127
		}
		if count > 0 {
			runs = append(runs, (prev<<7)|byte(count))
		}
	}
	return runs
}

func packLine(line []byte) []byte {
	out := make([]byte, PaperWidth/8)
	for i := 0; i < len(line); i += 8 {
		var b byte
		for bit := 0; bit < 8; bit++ {
			if line[i+bit] == 1 {
				b |= (1 << bit)
			}
		}
		out[i/8] = b
	}
	return out
}

func loadConfig() {
	exe, err := os.Executable()
	path := ConfigFile
	if err == nil {
		path = filepath.Join(filepath.Dir(exe), ConfigFile)
	}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &appConfig)
	}
}

func saveConfig() {
	exe, err := os.Executable()
	path := ConfigFile
	if err == nil {
		path = filepath.Join(filepath.Dir(exe), ConfigFile)
	}
	if data, err := json.Marshal(appConfig); err == nil {
		os.WriteFile(path, data, 0644)
	}
}
