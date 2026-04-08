package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fogleman/gg"
	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/skip2/go-qrcode"
)

//go:embed dashboard.html
var dashboardHTML embed.FS

const (
	ConfigFile = "config.json"
	PaperWidth = 384
)

type Config struct {
	MAC string `json:"mac"`
}

var (
	appConfig = Config{MAC: "c4:76:44:3f:7f:e3"}
	clients   = make(map[chan string]bool)
	clientsMu sync.Mutex
	bleMu     sync.Mutex // only one BLE op at a time
)

func logMsg(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	fmt.Print(msg)
	go func() {
		clientsMu.Lock()
		defer clientsMu.Unlock()
		for c := range clients {
			select {
			case c <- msg:
			default:
			}
		}
	}()
}

func main() {
	loadConfig()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down QR-Printer-Go...")
		os.Exit(0)
	}()

	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/print", handlePrint)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/events", handleEvents)
	http.HandleFunc("/reset", handleReset)

	port := "2030"
	fmt.Printf("\n🚀 [QR-Printer-Go] Dashboard Ready!\n")
	fmt.Printf("📍 Dashboard & API: http://localhost:%s\n", port)
	fmt.Printf("🖨️  Active Printer: %s\n\n", appConfig.MAC)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

// --- HANDLERS ---

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
		// go-ble expects lowercase MAC addresses
		appConfig.MAC = strings.ToLower(newCfg.MAC)
		saveConfig()
		fmt.Printf("[Config] Updated MAC to: %s\n", appConfig.MAC)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(appConfig)
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	c := make(chan string, 20)
	clientsMu.Lock()
	clients[c] = true
	clientsMu.Unlock()

	defer func() {
		clientsMu.Lock()
		delete(clients, c)
		clientsMu.Unlock()
		close(c)
	}()

	for {
		select {
		case msg := <-c:
			fmt.Fprintf(w, "data: %s\n\n", strings.TrimSpace(msg))
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func handleReset(w http.ResponseWriter, r *http.Request) {
	logMsg("[System] Resetting Bluetooth radio...\n")
	exec.Command("hciconfig", "hci0", "down").Run()
	time.Sleep(500 * time.Millisecond)
	exec.Command("hciconfig", "hci0", "up").Run()
	time.Sleep(500 * time.Millisecond)
	logMsg("[System] Radio reset done. Try printing now.\n")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"success": true}`))
}

func handlePrint(w http.ResponseWriter, r *http.Request) {
	qrText := r.URL.Query().Get("qr")
	msgText := r.URL.Query().Get("text")

	if qrText == "" && msgText == "" {
		http.Error(w, `{"error": "Missing parameters"}`, http.StatusBadRequest)
		return
	}

	logMsg("[Server] Print Request: QR='%s', Text='%s'\n", qrText, msgText)

	pixels, height, err := generateImage(qrText, msgText)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
		return
	}

	if err := printBLE(pixels, height); err != nil {
		logMsg("[Error] %v\n", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}

// --- PRINTER LOGIC ---

func generateImage(qrText, msgText string) ([]byte, int, error) {
	const qrSize = 320
	var qrImg image.Image
	var err error

	if qrText != "" {
		var q *qrcode.QRCode
		q, err = qrcode.New(qrText, qrcode.Medium)
		if err != nil {
			return nil, 0, err
		}
		q.DisableBorder = true
		qrImg = q.Image(qrSize)
	}

	if msgText == "" && qrText != "" {
		msgText = qrText
	}

	const fontSize = 32
	dc := gg.NewContext(PaperWidth, 1000)
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

	var lines []string
	if msgText != "" {
		lines = dc.WordWrap(msgText, PaperWidth-40)
	}

	lineHeight := fontSize + 12
	textHeight := 0
	if msgText != "" {
		textHeight = len(lines)*lineHeight + 60
	}

	actualQrSize := 0
	if qrImg != nil {
		actualQrSize = qrSize
	}

	totalHeight := actualQrSize + textHeight + 40
	if totalHeight < 40 {
		totalHeight = 40
	}

	dest := gg.NewContext(PaperWidth, totalHeight)
	dest.SetColor(color.White)
	dest.Clear()

	if fontLoaded && msgText != "" {
		for _, p := range fontPaths {
			if err := dest.LoadFontFace(p, fontSize); err == nil {
				break
			}
		}
	}

	textStartY := 40.0
	if qrImg != nil {
		dest.DrawImage(qrImg, (PaperWidth-actualQrSize)/2, 20)
		textStartY = float64(actualQrSize + 60)
	}

	dest.SetColor(color.Black)
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

// printBLE uses go-ble/ble which communicates via raw HCI sockets,
// completely bypassing BlueZ. This eliminates all "No more profiles" and
// "br-connection-refused" errors — those are BlueZ-layer problems that
// simply do not exist at the HCI level. HCI LE_Create_Connection is
// LE-only by definition.
func printBLE(pixels []byte, height int) error {
	// go-ble takes exclusive ownership of the HCI adapter, so serialize calls.
	bleMu.Lock()
	defer bleMu.Unlock()

	targetMAC := strings.ToLower(appConfig.MAC)

	logMsg("[Printer] Initialising HCI device...\n")
	d, err := linux.NewDevice()
	if err != nil {
		return fmt.Errorf("HCI init: %w (try: sudo setcap 'cap_net_raw,cap_net_admin=eip' ./qr-printer)", err)
	}
	ble.SetDefaultDevice(d)
	defer d.Stop()

	// ble.Connect scans (HCI_LE_Set_Scan_Enable) then connects
	// (HCI_LE_Create_Connection) — both are LE-only HCI commands.
	logMsg("[Printer] Scanning and connecting to %s...\n", targetMAC)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cln, err := ble.Connect(ctx, func(a ble.Advertisement) bool {
		return strings.EqualFold(a.Addr().String(), targetMAC)
	})
	if err != nil {
		if err == context.DeadlineExceeded {
			return fmt.Errorf("printer not found after 15s — is it powered on?")
		}
		return fmt.Errorf("connect: %w", err)
	}
	defer cln.CancelConnection()

	logMsg("[Printer] Connected. Discovering GATT profile...\n")
	p, err := cln.DiscoverProfile(true)
	if err != nil {
		return fmt.Errorf("discover profile: %w", err)
	}

	// Find write characteristic (AE01 or AE02).
	var writeChar *ble.Characteristic
	allUUIDs := []string{}
	for _, svc := range p.Services {
		for _, c := range svc.Characteristics {
			uuid := strings.ToLower(c.UUID.String())
			allUUIDs = append(allUUIDs, uuid)
			if strings.Contains(uuid, "ae01") {
				writeChar = c
				break // ae01 is the write char, ae02 is notify — stop here
			}
		}
		if writeChar != nil {
			break
		}
	}
	// fallback to ae02 only if ae01 not found
	if writeChar == nil {
		for _, svc := range p.Services {
			for _, c := range svc.Characteristics {
				if strings.Contains(strings.ToLower(c.UUID.String()), "ae02") {
					writeChar = c
				}
			}
		}
	}

	if writeChar == nil {
		logMsg("[Error] Available characteristics: %v\n", strings.Join(allUUIDs, ", "))
		return fmt.Errorf("characteristic AE01/AE02 not found")
	}
	logMsg("[Printer] Using characteristic: %s\n", writeChar.UUID.String())

	// Build print job.
	var job []byte
	job = append(job, makePacket(0xA4, []byte{0x33})...)       // Blackening level 3
	job = append(job, makePacket(0xAF, []byte{0x1C, 0x25})...) // Energy 9500 (0x251C LE)
	job = append(job, makePacket(0xBE, []byte{0x00})...)       // Mode: image
	job = append(job, makePacket(0xBD, []byte{0x0A})...)       // Speed: img_print_speed=10

	for y := 0; y < height; y++ {
		line := pixels[y*PaperWidth : (y+1)*PaperWidth]
		rle := encodeRLE(line)
		if len(rle) <= PaperWidth/8 {
			job = append(job, makePacket(0xBF, rle)...)
		} else {
			job = append(job, makePacket(0xA2, packLine(line))...)
		}
	}

	job = append(job, makePacket(0xBD, []byte{0x0A})...)       // Feed speed (end)
	job = append(job, makePacket(0xA1, []byte{0x30, 0x00})...) // Paper type — triggers physical print

	// Write in chunks. noRsp=true → WriteWithoutResponse over raw HCI.
	logMsg("[Printer] Sending %d bytes...\n", len(job))
	mtu := 180                         // img_mtu from X6H spec (was 123)
	chunkDelay := 4 * time.Millisecond // interval_ms from X6H spec (was 6ms)
	for i := 0; i < len(job); i += mtu {
		end := i + mtu
		if end > len(job) {
			end = len(job)
		}
		if err := cln.WriteCharacteristic(writeChar, job[i:end], true); err != nil {
			return fmt.Errorf("write at offset %d: %w", i, err)
		}
		time.Sleep(chunkDelay)
	}

	logMsg("[Printer] Success\n")
	return nil
}

// --- PACKET HELPERS ---

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
	// X6H has new_format = false, so no 0x12 prepended
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

// --- CONFIG ---

func loadConfig() {
	exe, err := os.Executable()
	path := ConfigFile
	if err == nil {
		path = filepath.Join(filepath.Dir(exe), ConfigFile)
	}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &appConfig)
	}
	appConfig.MAC = strings.ToLower(appConfig.MAC)
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
