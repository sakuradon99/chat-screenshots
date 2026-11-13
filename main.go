package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var assets embed.FS

const (
	maxFileBytes    = 32 << 20
	maxSourcePixels = 24_000_000
	maxExportPixels = 500_000_000
	maxImages       = 300
	maxExportWidth  = 4096
)

type screenshot struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
}

type group struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Date     string       `json:"date"`
	Revision uint64       `json:"revision"`
	Images   []screenshot `json:"images"`
	Crop     *cropRegion  `json:"crop,omitempty"`
}

type state struct {
	Revision uint64  `json:"revision"`
	Groups   []group `json:"groups"`
}

type app struct {
	mu        sync.Mutex
	state     state
	dir       string
	port      string
	accessURL string
	uploads   chan struct{}
	exports   chan struct{}
}

type clientError struct {
	status  int
	message string
}

func (e *clientError) Error() string { return e.message }

func invalid(message string) error { return &clientError{http.StatusBadRequest, message} }

var missingGroup = &clientError{http.StatusNotFound, "这个截图组已不存在。"}
var missingImage = &clientError{http.StatusNotFound, "这张图片已不存在。"}
var orderChanged = &clientError{http.StatusConflict, "这个组刚刚在其他设备上更新了。已同步最新顺序，请再试一次。"}

func main() {
	addr := flag.String("addr", "0.0.0.0:8080", "HTTP listen address")
	data := flag.String("data", "data", "Directory for groups and screenshots")
	flag.Parse()

	a, err := openApp(*data)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	boundHost, port, _ := net.SplitHostPort(listener.Addr().String())
	a.port = port
	a.accessURL = os.Getenv("CHAT_SCREENSHOTS_ACCESS_URL")
	desktopHost := boundHost
	if ip := net.ParseIP(boundHost); ip != nil && ip.IsUnspecified() {
		desktopHost = "127.0.0.1"
	}
	log.Printf("电脑  http://%s", net.JoinHostPort(desktopHost, port))
	if ip := net.ParseIP(boundHost); ip != nil && !ip.IsLoopback() {
		for _, url := range a.lanURLs() {
			log.Printf("手机  %s", url)
		}
	}
	log.Printf("数据  %s", a.dir)
	server := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.Serve(listener))
}

func openApp(dir string) (*app, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0700); err != nil {
		return nil, err
	}
	a := &app{
		dir: dir, state: state{Groups: []group{}},
		uploads: make(chan struct{}, 2), exports: make(chan struct{}, 1),
	}
	raw, err := os.ReadFile(filepath.Join(dir, "groups.json"))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &a.state); err != nil {
		return nil, fmt.Errorf("读取截图组: %w", err)
	}
	return a, nil
}

// saveLocked publishes a new snapshot only after the disk write succeeds.
func (a *app) saveLocked(groups []group) error {
	next := state{Revision: a.state.Revision + 1, Groups: groups}
	f, err := os.CreateTemp(a.dir, "groups-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(next)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(a.dir, "groups.json")); err != nil {
		return err
	}
	a.state = next
	return nil
}

func (a *app) updateGroup(id string, update func(*group) error) (group, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.groupIndex(id)
	if i < 0 {
		return group{}, missingGroup
	}
	groups := slices.Clone(a.state.Groups)
	g := &groups[i]
	g.Images = slices.Clone(g.Images)
	if err := update(g); err != nil {
		return group{}, err
	}
	g.Revision++
	return *g, a.saveLocked(groups)
}

func (a *app) groupIndex(id string) int {
	return slices.IndexFunc(a.state.Groups, func(g group) bool { return g.ID == id })
}

func newID() string {
	var id [16]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	web, err := fs.Sub(assets, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("GET /api/groups", a.list)
	mux.HandleFunc("GET /api/access", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, a.lanURLs())
	})
	mux.HandleFunc("POST /api/groups", a.saveGroup)
	mux.HandleFunc("PATCH /api/groups/{group}", a.saveGroup)
	mux.HandleFunc("DELETE /api/groups/{group}", a.deleteGroup)
	mux.HandleFunc("POST /api/groups/{group}/images", a.upload)
	mux.HandleFunc("PUT /api/groups/{group}/order", a.order)
	mux.HandleFunc("PUT /api/groups/{group}/crop", a.saveCrop)
	mux.HandleFunc("GET /api/groups/{group}/images/{image}", a.getImage)
	mux.HandleFunc("DELETE /api/groups/{group}/images/{image}", a.deleteImage)
	mux.HandleFunc("GET /api/groups/{group}/export", a.export)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-cache")
		host := r.Host
		if value, _, err := net.SplitHostPort(host); err == nil {
			host = value
		}
		if host != "localhost" && host != "chat-screenshots" && net.ParseIP(strings.Trim(host, "[]")) == nil {
			writeError(w, invalid("请用 localhost 或电脑 IP 地址访问。"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Chat-Screenshots") != "1" {
			writeError(w, &clientError{http.StatusForbidden, "请从工作台提交更改。"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *app) lanURLs() []string {
	if a.accessURL != "" {
		return []string{a.accessURL}
	}
	urls := []string{}
	interfaces, _ := net.Interfaces()
	for _, nic := range interfaces {
		if nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := nic.Addrs()
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() {
				urls = append(urls, "http://"+net.JoinHostPort(ip.String(), a.port))
			}
		}
	}
	return urls
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("写入响应: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error) {
	var client *clientError
	if errors.As(err, &client) {
		writeJSON(w, client.status, map[string]string{"error": client.message})
		return
	}
	log.Print(err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "服务器处理失败，请稍后重试。"})
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := reader.Decode(target); err != nil {
		return invalid("提交的数据格式有误。")
	}
	if err := reader.Decode(new(any)); err != io.EOF {
		return invalid("提交的数据格式有误。")
	}
	return nil
}

func (a *app) list(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	snapshot := a.state
	a.mu.Unlock()
	tag := fmt.Sprintf("%q", strconv.FormatUint(snapshot.Revision, 10))
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *app) saveGroup(w http.ResponseWriter, r *http.Request) {
	var fields struct{ Name, Date string }
	if err := readJSON(w, r, &fields); err != nil {
		writeError(w, err)
		return
	}
	fields.Name = strings.TrimSpace(fields.Name)
	if fields.Name == "" || utf8.RuneCountInString(fields.Name) > 80 {
		writeError(w, invalid("组名需要填写，最多 80 个字。"))
		return
	}
	if _, err := time.Parse("2006-01-02", fields.Date); err != nil || fields.Date < "0001-01-01" {
		writeError(w, invalid("请选择有效日期。"))
		return
	}
	if r.Method == http.MethodPatch {
		updated, err := a.updateGroup(r.PathValue("group"), func(g *group) error {
			g.Name, g.Date = fields.Name, fields.Date
			return nil
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
		return
	}
	created := group{ID: newID(), Name: fields.Name, Date: fields.Date, Revision: 1, Images: []screenshot{}}
	a.mu.Lock()
	err := a.saveLocked(append(slices.Clone(a.state.Groups), created))
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *app) imagePath(id string) string {
	return filepath.Join(a.dir, "images", id)
}

func (a *app) removeImage(id string) {
	if err := os.Remove(a.imagePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("删除图片文件: %v", err)
	}
}

func (a *app) deleteGroup(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	i := a.groupIndex(r.PathValue("group"))
	if i < 0 {
		a.mu.Unlock()
		writeError(w, missingGroup)
		return
	}
	removed := a.state.Groups[i]
	err := a.saveLocked(slices.Delete(slices.Clone(a.state.Groups), i, i+1))
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	for _, img := range removed.Images {
		a.removeImage(img.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) upload(w http.ResponseWriter, r *http.Request) {
	select {
	case a.uploads <- struct{}{}:
		defer func() { <-a.uploads }()
	case <-r.Context().Done():
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFileBytes)
	staged, err := os.CreateTemp(filepath.Join(a.dir, "images"), "upload-*.tmp")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if _, err := io.Copy(staged, r.Body); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeError(w, &clientError{http.StatusRequestEntityTooLarge, "单张图片不能超过 32 MB。"})
		} else {
			writeError(w, err)
		}
		return
	}
	config, format, err := image.DecodeConfig(io.NewSectionReader(staged, 0, maxFileBytes))
	if err != nil || config.Width < 1 || config.Height < 1 {
		writeError(w, invalid("无法读取这张图片，请使用 PNG、JPEG 或 GIF。"))
		return
	}
	if config.Width > maxSourcePixels/config.Height {
		writeError(w, invalid("单张图片不能超过 2400 万像素。"))
		return
	}
	if _, _, err := image.Decode(io.NewSectionReader(staged, 0, maxFileBytes)); err != nil {
		writeError(w, invalid("图片文件不完整，无法读取。"))
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "截图"
	}
	if letters := []rune(name); len(letters) > 180 {
		name = string(letters[:180])
	}
	img := screenshot{ID: newID(), Name: name, Width: config.Width, Height: config.Height, Format: format}
	if err := errors.Join(staged.Sync(), staged.Close()); err != nil {
		writeError(w, err)
		return
	}
	_, err = a.updateGroup(r.PathValue("group"), func(g *group) error {
		if len(g.Images) >= maxImages {
			return invalid("每组最多可以添加 300 张图片。")
		}
		if err := os.Rename(staged.Name(), a.imagePath(img.ID)); err != nil {
			return err
		}
		g.Images = append(g.Images, img)
		if g.Crop != nil && (img.Width != g.Images[0].Width || img.Height != g.Images[0].Height) {
			g.Crop = nil
		}
		return nil
	})
	if err != nil {
		a.removeImage(img.ID)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, img)
}

func (a *app) deleteImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("image")
	_, err := a.updateGroup(r.PathValue("group"), func(g *group) error {
		i := slices.IndexFunc(g.Images, func(img screenshot) bool { return img.ID == id })
		if i < 0 {
			return missingImage
		}
		g.Images = slices.Delete(g.Images, i, i+1)
		if len(g.Images) == 0 {
			g.Crop = nil
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	a.removeImage(id)
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) getImage(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	i := a.groupIndex(r.PathValue("group"))
	if i < 0 {
		a.mu.Unlock()
		writeError(w, missingGroup)
		return
	}
	g := a.state.Groups[i]
	images := g.Images
	id := r.PathValue("image")
	j := slices.IndexFunc(images, func(img screenshot) bool { return img.ID == id })
	if j < 0 {
		a.mu.Unlock()
		writeError(w, missingImage)
		return
	}
	img := images[j]
	file, err := os.Open(a.imagePath(id))
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	defer file.Close()
	if r.URL.Query().Get("crop") == "1" && g.Crop != nil {
		tag := fmt.Sprintf("%q", fmt.Sprintf("%s-%d-%d-%d-%d", img.ID, g.Crop.X, g.Crop.Y, g.Crop.Width, g.Crop.Height))
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		cropped, _, err := image.Decode(file)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(w, g.Crop.apply(cropped)); err != nil {
			log.Printf("输出裁切预览: %v", err)
		}
		return
	}
	w.Header().Set("Content-Type", "image/"+img.Format)
	w.Header().Set("ETag", fmt.Sprintf("%q", img.ID))
	http.ServeContent(w, r, img.Name, time.Time{}, file)
}

func (a *app) order(w http.ResponseWriter, r *http.Request) {
	var fields struct {
		Revision uint64
		IDs      []string
	}
	if err := readJSON(w, r, &fields); err != nil {
		writeError(w, err)
		return
	}
	updated, err := a.updateGroup(r.PathValue("group"), func(g *group) error {
		if g.Revision != fields.Revision || len(fields.IDs) != len(g.Images) {
			return orderChanged
		}
		byID := make(map[string]screenshot, len(g.Images))
		for _, img := range g.Images {
			byID[img.ID] = img
		}
		for i, id := range fields.IDs {
			img, ok := byID[id]
			if !ok {
				return invalid("图片顺序包含重复或无效的项目。")
			}
			g.Images[i] = img
			delete(byID, id)
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

type exportSource struct {
	file   *os.File
	height int
	crop   *cropRegion
}

func (a *app) export(w http.ResponseWriter, r *http.Request) {
	width, err := strconv.Atoi(r.URL.Query().Get("width"))
	if err != nil || width < 1 || width > maxExportWidth {
		writeError(w, invalid("请输入 1 到 4096 像素之间的宽度。"))
		return
	}
	select {
	case a.exports <- struct{}{}:
		defer func() { <-a.exports }()
	case <-r.Context().Done():
		return
	}
	g, sources, height, err := a.exportSnapshot(r.PathValue("group"), width)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() {
		for _, source := range sources {
			source.file.Close()
		}
	}()
	filename := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune("<>:\"/\\|?*", r) {
			return '_'
		}
		return r
	}, g.Name)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": g.Date + " " + filename + ".png",
	}))
	w.Header().Set("Cache-Control", "no-store")
	if err := writeLongPNG(r.Context(), w, width, height, sources); err != nil {
		if r.Context().Err() == nil {
			log.Printf("输出长图: %v", err)
		}
		panic(http.ErrAbortHandler)
	}
}

func (a *app) exportSnapshot(id string, width int) (group, []exportSource, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.groupIndex(id)
	if i < 0 {
		return group{}, nil, 0, missingGroup
	}
	g := a.state.Groups[i]
	if len(g.Images) == 0 {
		return group{}, nil, 0, invalid("请先添加至少一张图片。")
	}
	height := 0
	sources := make([]exportSource, len(g.Images))
	for i, img := range g.Images {
		sourceWidth, sourceHeight := img.Width, img.Height
		if g.Crop != nil {
			sourceWidth, sourceHeight = g.Crop.Width, g.Crop.Height
		}
		sources[i].crop = g.Crop
		sources[i].height = max(1, int(math.Round(float64(sourceHeight)*float64(width)/float64(sourceWidth))))
		if sources[i].height > maxExportPixels/width-height {
			return group{}, nil, 0, invalid("长图超过 5 亿像素，请调小宽度后重新导出。")
		}
		height += sources[i].height
	}
	for i, img := range g.Images {
		file, err := os.Open(a.imagePath(img.ID))
		if err != nil {
			for _, opened := range sources[:i] {
				opened.file.Close()
			}
			return group{}, nil, 0, err
		}
		sources[i].file = file
	}
	return g, sources, height, nil
}

type sample struct {
	first, second int
	fraction      float64
}

func sampleAt(index, outputSize, inputMin, inputSize int) sample {
	pos := max(0, (float64(index)+0.5)*float64(inputSize)/float64(outputSize)-0.5)
	first := int(pos)
	return sample{inputMin + first, inputMin + min(first+1, inputSize-1), pos - float64(first)}
}

func blend(a, b, c, d uint32, x, y float64) uint8 {
	top := float64(a)*(1-x) + float64(b)*x
	bottom := float64(c)*(1-x) + float64(d)*x
	return uint8(math.Round((top*(1-y) + bottom*y) / 257))
}
