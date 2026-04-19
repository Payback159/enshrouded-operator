/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package main is the entry-point for the enshrouded-metrics-sidecar.
//
// The sidecar is injected into the Enshrouded game server pod by the operator
// when metricsSidecar.enabled=true is set on the EnshroudedServer resource.
// It periodically queries the game server via the Steam A2S query protocol
// (UDP, same pod → localhost) and:
//
//  1. Exposes Prometheus metrics on /metrics (default port :9090).
//  2. Patches the EnshroudedServer CR status with the live ActivePlayers count
//     so the operator's DeferWhilePlaying feature has accurate data.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	enshroudedv1alpha1 "github.com/payback159/enshrouded-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ──────────────────────────────────────────────────────────────────────────────
// Configuration (from environment variables)
// ──────────────────────────────────────────────────────────────────────────────

type config struct {
	queryHost          string
	queryPort          int
	metricsAddr        string
	scrapeInterval     time.Duration
	serverNamespace    string
	serverName         string
	patchStatusEnabled bool
}

func loadConfig() config {
	port, _ := strconv.Atoi(envOrDefault("QUERY_PORT", "15637"))
	if port <= 0 {
		port = 15637
	}
	interval, err := time.ParseDuration(envOrDefault("SCRAPE_INTERVAL", "15s"))
	if err != nil || interval < 5*time.Second {
		interval = 15 * time.Second
	}
	patchEnabled := true
	if v := os.Getenv("STATUS_PATCH_ENABLED"); v == "false" || v == "0" {
		patchEnabled = false
	}
	return config{
		queryHost:          envOrDefault("QUERY_HOST", "127.0.0.1"),
		queryPort:          port,
		metricsAddr:        envOrDefault("METRICS_ADDR", ":9090"),
		scrapeInterval:     interval,
		serverNamespace:    os.Getenv("SERVER_NAMESPACE"),
		serverName:         os.Getenv("SERVER_NAME"),
		patchStatusEnabled: patchEnabled,
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ──────────────────────────────────────────────────────────────────────────────
// Steam A2S query protocol implementation
// ──────────────────────────────────────────────────────────────────────────────

// a2sHeader is the 4-byte Source Engine out-of-band packet prefix.
var a2sHeader = []byte{0xFF, 0xFF, 0xFF, 0xFF}

// buildA2SInfoRequest constructs an A2S_INFO request.
// challenge must be 4 bytes; use {0xFF, 0xFF, 0xFF, 0xFF} for the initial request.
func buildA2SInfoRequest(challenge []byte) []byte {
	const payload = "Source Engine Query\x00"
	req := make([]byte, 0, 4+1+len(payload)+4)
	req = append(req, a2sHeader...)
	req = append(req, 0x54) // A2S_INFO request type
	req = append(req, []byte(payload)...)
	req = append(req, challenge...)
	return req
}

// A2SInfo holds the fields we extract from an A2S_INFO response.
type A2SInfo struct {
	Name       string
	MapName    string
	Players    uint8
	MaxPlayers uint8
	Version    string
}

// queryA2SInfo sends an A2S_INFO query to addr and returns the parsed response.
// It handles the challenge-response handshake introduced in newer Source servers.
//
// We use an unconnected UDP socket (net.ListenPacket + WriteTo/ReadFrom) instead
// of net.DialTimeout so that ICMP port-unreachable errors — which Linux delivers
// as ECONNREFUSED on connected sockets — cannot poison the query. This avoids a
// common failure mode where a brief server unavailability leaves a sticky error
// on a connected socket that persists until the socket is closed.
func queryA2SInfo(addr string, timeout time.Duration) (*A2SInfo, error) {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", addr, err)
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("opening UDP socket: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	buf := make([]byte, 4096)

	// Initial request with placeholder challenge 0xFFFFFFFF.
	req := buildA2SInfoRequest([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	resp, err := udpRoundTrip(conn, req, buf, raddr, timeout)
	if err != nil {
		return nil, err
	}

	// If the server returns a challenge packet (type 0x41 = 'A'), retry with the
	// provided challenge bytes. This is required by newer Source Engine servers.
	if len(resp) >= 9 && bytes.Equal(resp[:4], a2sHeader) && resp[4] == 0x41 {
		challenge := resp[5:9]
		req = buildA2SInfoRequest(challenge)
		resp, err = udpRoundTrip(conn, req, buf, raddr, timeout)
		if err != nil {
			return nil, fmt.Errorf("A2S_INFO (after challenge): %w", err)
		}
	}

	return parseA2SInfo(resp)
}

// udpRoundTrip sends req to raddr via the unconnected PacketConn and waits for
// a response from raddr. Using WriteTo/ReadFrom instead of a connected socket
// avoids ICMP-error poisoning on Linux.
func udpRoundTrip(conn net.PacketConn, req, buf []byte, raddr net.Addr, timeout time.Duration) ([]byte, error) {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if _, err := conn.WriteTo(req, raddr); err != nil {
		return nil, fmt.Errorf("sending A2S_INFO request: %w", err)
	}
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, fmt.Errorf("reading A2S_INFO response: %w", err)
		}
		// Discard packets from unexpected sources.
		if from.String() == raddr.String() {
			return buf[:n], nil
		}
	}
}

// parseA2SInfo parses a raw A2S_INFO response packet.
func parseA2SInfo(data []byte) (*A2SInfo, error) {
	// Minimum valid response: 4 header + 1 type + 1 protocol = 6 bytes
	if len(data) < 6 {
		return nil, fmt.Errorf("A2S_INFO response too short: %d bytes", len(data))
	}
	if !bytes.Equal(data[:4], a2sHeader) {
		return nil, fmt.Errorf("A2S_INFO: invalid header 0x%X", data[:4])
	}
	if data[4] != 0x49 {
		return nil, fmt.Errorf("A2S_INFO: unexpected type byte 0x%02X (expected 0x49)", data[4])
	}

	// pos starts at 6: skip header(4) + type(1) + protocol version(1)
	r := &byteReader{data: data, pos: 6}

	name, err := r.readString()
	if err != nil {
		return nil, fmt.Errorf("reading server name: %w", err)
	}
	mapName, err := r.readString()
	if err != nil {
		return nil, fmt.Errorf("reading map name: %w", err)
	}
	if _, err := r.readString(); err != nil { // folder
		return nil, fmt.Errorf("reading folder: %w", err)
	}
	if _, err := r.readString(); err != nil { // game
		return nil, fmt.Errorf("reading game: %w", err)
	}
	r.skip(2)                     // app_id (uint16 LE)
	players, _ := r.readByte()    // num_players
	maxPlayers, _ := r.readByte() // max_players
	r.skip(1)                     // num_bots
	r.skip(1)                     // server_type
	r.skip(1)                     // environment
	r.skip(1)                     // visibility
	r.skip(1)                     // VAC
	version, _ := r.readString()

	return &A2SInfo{
		Name:       name,
		MapName:    mapName,
		Players:    players,
		MaxPlayers: maxPlayers,
		Version:    version,
	}, nil
}

// byteReader is a minimal cursor over a byte slice for sequential parsing.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) readByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func (r *byteReader) readUint16LE() (uint16, error) { //nolint:unused
	if r.pos+2 > len(r.data) {
		return 0, io.EOF
	}
	v := binary.LittleEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *byteReader) readString() (string, error) {
	start := r.pos
	for r.pos < len(r.data) {
		if r.data[r.pos] == 0 {
			s := string(r.data[start:r.pos])
			r.pos++ // consume null terminator
			return s, nil
		}
		r.pos++
	}
	return "", fmt.Errorf("unterminated string starting at byte %d", start)
}

func (r *byteReader) skip(n int) {
	r.pos += n
}

// ──────────────────────────────────────────────────────────────────────────────
// Prometheus metrics registry
// ──────────────────────────────────────────────────────────────────────────────

type metrics struct {
	serverUp      *prometheus.GaugeVec
	activePlayers *prometheus.GaugeVec
	maxPlayers    *prometheus.GaugeVec
	serverInfo    *prometheus.GaugeVec // always 1 when up; labels carry name/map/version
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		serverUp: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "enshrouded",
				Name:      "server_up",
				Help:      "1 if the game server responds to A2S queries, 0 otherwise.",
			},
			[]string{"server_name"},
		),
		activePlayers: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "enshrouded",
				Name:      "active_players",
				Help:      "Number of players currently connected to the game server.",
			},
			[]string{"server_name"},
		),
		maxPlayers: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "enshrouded",
				Name:      "max_players",
				Help:      "Maximum number of player slots configured on the game server.",
			},
			[]string{"server_name"},
		),
		serverInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "enshrouded",
				Name:      "server_info",
				Help:      "Static server information. Value is always 1 when the server is up.",
			},
			[]string{"server_name", "map", "version"},
		),
	}
	reg.MustRegister(m.serverUp, m.activePlayers, m.maxPlayers, m.serverInfo)
	return m
}

// ──────────────────────────────────────────────────────────────────────────────
// Kubernetes status patcher
// ──────────────────────────────────────────────────────────────────────────────

// buildK8sClient creates a controller-runtime client using in-cluster credentials.
// Returns nil (not an error) when running outside a cluster — status patching is
// then silently skipped.
func buildK8sClient() (client.Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Not running inside a pod — status patching not available.
		return nil, nil //nolint:nilerr
	}
	scheme := runtime.NewScheme()
	if err := enshroudedv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering EnshroudedServer scheme: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating K8s client: %w", err)
	}
	return c, nil
}

// patchServerStatus writes the current player count and game version into the
// EnshroudedServer CR status. It reads the current values first and skips the
// patch when nothing changed to avoid spurious reconcile triggers.
func patchServerStatus(
	ctx context.Context,
	k8sClient client.Client,
	ns, name string,
	players int32,
	gameVersion string,
) {
	server := &enshroudedv1alpha1.EnshroudedServer{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, server); err != nil {
		slog.Warn("failed to get EnshroudedServer for status patch", "err", err)
		return
	}
	if server.Status.ActivePlayers == players && server.Status.GameVersion == gameVersion {
		return // no change — skip patch to avoid spurious reconcile triggers
	}
	patch := client.MergeFrom(server.DeepCopy())
	server.Status.ActivePlayers = players
	server.Status.GameVersion = gameVersion
	if err := k8sClient.Status().Patch(ctx, server, patch); err != nil {
		slog.Warn("failed to patch EnshroudedServer status", "err", err,
			"namespace", ns, "name", name, "activePlayers", players, "gameVersion", gameVersion)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Scrape loop
// ──────────────────────────────────────────────────────────────────────────────

func scrape(
	ctx context.Context,
	cfg config,
	m *metrics,
	k8sClient client.Client,
	previousInfo *A2SInfo,
	everUp bool,
	isUp *atomic.Bool,
) (*A2SInfo, bool) {
	addr := fmt.Sprintf("%s:%d", cfg.queryHost, cfg.queryPort)
	info, err := queryA2SInfo(addr, 3*time.Second)
	if err != nil {
		// Log at WARN only after the server has been up at least once.
		// During initial startup (SteamCMD download, world generation) we use
		// DEBUG to avoid noisy logs in normal operation.
		if everUp {
			slog.Warn("A2S query failed", "addr", addr, "err", err)
		} else {
			slog.Debug("A2S query failed (server not yet up)", "addr", addr, "err", err)
		}
		m.serverUp.WithLabelValues(cfg.serverName).Set(0)
		m.activePlayers.WithLabelValues(cfg.serverName).Set(0)
		isUp.Store(false)
		// Patch zero players on failure (server unreachable).
		if k8sClient != nil && cfg.patchStatusEnabled && cfg.serverNamespace != "" && cfg.serverName != "" {
			patchServerStatus(ctx, k8sClient, cfg.serverNamespace, cfg.serverName, 0, "")
		}
		return nil, everUp
	}

	slog.Debug("A2S query successful",
		"server", info.Name,
		"map", info.MapName,
		"players", info.Players,
		"maxPlayers", info.MaxPlayers,
		"version", info.Version,
	)

	if !everUp {
		slog.Info("A2S query succeeded — server is up", "server", info.Name, "version", info.Version)
	}

	m.serverUp.WithLabelValues(cfg.serverName).Set(1)
	m.activePlayers.WithLabelValues(cfg.serverName).Set(float64(info.Players))
	m.maxPlayers.WithLabelValues(cfg.serverName).Set(float64(info.MaxPlayers))
	isUp.Store(true)

	// Only update the info gauge when the label values change (to avoid churn).
	if previousInfo == nil ||
		previousInfo.MapName != info.MapName ||
		previousInfo.Version != info.Version {
		if previousInfo != nil {
			m.serverInfo.DeleteLabelValues(cfg.serverName, previousInfo.MapName, previousInfo.Version)
		}
		m.serverInfo.WithLabelValues(cfg.serverName, info.MapName, info.Version).Set(1)
	}

	// Patch CR status so the operator can use the live player count and game version.
	if k8sClient != nil && cfg.patchStatusEnabled && cfg.serverNamespace != "" && cfg.serverName != "" {
		patchServerStatus(ctx, k8sClient, cfg.serverNamespace, cfg.serverName, int32(info.Players), info.Version)
	}

	return info, true
}

// ──────────────────────────────────────────────────────────────────────────────
// Main
// ──────────────────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()

	slog.Info("enshrouded-metrics-sidecar starting",
		"queryHost", cfg.queryHost,
		"queryPort", cfg.queryPort,
		"metricsAddr", cfg.metricsAddr,
		"scrapeInterval", cfg.scrapeInterval,
		"serverNamespace", cfg.serverNamespace,
		"serverName", cfg.serverName,
		"patchStatusEnabled", cfg.patchStatusEnabled,
	)

	// Prometheus registry.
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)

	// Kubernetes client (optional — disabled when running outside a cluster).
	k8sClient, err := buildK8sClient()
	if err != nil {
		slog.Warn("Kubernetes client unavailable, status patching disabled", "err", err)
	}

	// isUp tracks whether the most recent A2S scrape succeeded.
	// Used by the /readyz endpoint to signal game server readiness.
	var isUp atomic.Bool

	// HTTP server for /metrics, /healthz and /readyz.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// /readyz returns 200 when the game server responds to A2S queries.
	// Used as the readiness probe for the game server container.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if isUp.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	srv := &http.Server{
		Addr:         cfg.metricsAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("metrics server listening", "addr", cfg.metricsAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Scrape loop.
	ctx := context.Background()
	ticker := time.NewTicker(cfg.scrapeInterval)
	defer ticker.Stop()

	var previousInfo *A2SInfo
	var everUp bool
	for range ticker.C {
		previousInfo, everUp = scrape(ctx, cfg, m, k8sClient, previousInfo, everUp, &isUp)
	}
}
