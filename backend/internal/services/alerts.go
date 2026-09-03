package services

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
)

const (
	// offlineFloor matches the dashboard's online/offline threshold, so an
	// "offline" rule can never fire before a card turns grey.
	offlineFloor = 2 * time.Minute
	// staleFloor is how long a threshold rule tolerates missing samples before
	// its open alert is resolved. Without it, a host that disappears while
	// breaching would keep an alert open forever.
	staleFloor           = 10 * time.Minute
	alertEventRetention  = 30 * 24 * time.Hour
	alertPruneInterval   = 6 * time.Hour
	webhookTimeout       = 6 * time.Second
	webhookMaxBodyLength = 4096
)

type alertKey struct {
	ruleID   uuid.UUID
	serverID uuid.UUID
}

// AlertState is the outcome of evaluating one rule against one server.
type AlertState int

const (
	// AlertClear means the metric was measurable and within bounds.
	AlertClear AlertState = iota
	// AlertBreached means the metric was measurable and out of bounds.
	AlertBreached
	// AlertUnknown means the metric could not be sampled (e.g. a disk rule on a
	// host that has not reported its disk size yet). Unknown never fires and
	// never resolves; it only cancels a pending countdown.
	AlertUnknown
)

// AlertEngine periodically evaluates threshold rules against the latest metric
// sample of every server and opens/resolves alert events accordingly.
type AlertEngine struct {
	db       *sql.DB
	interval time.Duration
	notifier *WebhookNotifier

	mu      sync.Mutex
	pending map[alertKey]time.Time
	firing  map[alertKey]struct{}

	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewAlertEngine(db *sql.DB, interval time.Duration, notifier *WebhookNotifier) *AlertEngine {
	if interval < time.Second {
		interval = 30 * time.Second
	}
	return &AlertEngine{
		db:       db,
		interval: interval,
		notifier: notifier,
		pending:  make(map[alertKey]time.Time),
		firing:   make(map[alertKey]struct{}),
		stopCh:   make(chan struct{}),
	}
}

func (e *AlertEngine) Start() {
	e.restoreFiringState()
	go func() {
		ticker := time.NewTicker(e.interval)
		defer ticker.Stop()
		prune := time.NewTicker(alertPruneInterval)
		defer prune.Stop()
		for {
			select {
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.evaluateOnce(time.Now())
			case <-prune.C:
				if err := models.PruneAlertEvents(e.db, time.Now().Add(-alertEventRetention)); err != nil {
					log.Printf("alerts: prune events: %v", err)
				}
			}
		}
	}()
}

func (e *AlertEngine) Stop() {
	e.stopOnce.Do(func() { close(e.stopCh) })
}

// restoreFiringState reloads open events so a restart neither re-notifies for
// alerts that are already firing nor resolves them twice.
func (e *AlertEngine) restoreFiringState() {
	open, err := models.GetOpenAlertKeys(e.db)
	if err != nil {
		log.Printf("alerts: restore firing state: %v", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for ruleID, servers := range open {
		for serverID := range servers {
			e.firing[alertKey{ruleID: ruleID, serverID: serverID}] = struct{}{}
		}
	}
}

func (e *AlertEngine) evaluateOnce(now time.Time) {
	rules, err := models.GetEnabledAlertRules(e.db)
	if err != nil {
		log.Printf("alerts: load rules: %v", err)
		return
	}
	if len(rules) == 0 {
		e.releaseStale(nil, now)
		return
	}
	snapshots, err := models.GetAlertSnapshots(e.db)
	if err != nil {
		log.Printf("alerts: load snapshots: %v", err)
		return
	}
	// A lookup failure only means this cycle notifies through per-rule URLs
	// alone; it must not stop evaluation.
	if defaults, err := models.GetDefaultWebhooks(e.db); err != nil {
		log.Printf("alerts: load default webhooks: %v", err)
	} else {
		ApplyDefaultWebhooks(rules, defaults)
	}
	live := make(map[alertKey]struct{})
	for _, rule := range rules {
		for _, snap := range snapshots {
			if snap.UserID != rule.UserID {
				continue
			}
			if rule.ServerID != nil && *rule.ServerID != snap.ServerID {
				continue
			}
			key := alertKey{ruleID: rule.ID, serverID: snap.ServerID}
			live[key] = struct{}{}
			e.applyRule(rule, snap, now)
		}
	}
	e.releaseStale(live, now)
}

// applyRule advances the debounce timer for one rule/server pair and
// opens or resolves its event when the state changes.
func (e *AlertEngine) applyRule(rule models.AlertRule, snap models.AlertSnapshot, now time.Time) {
	key := alertKey{ruleID: rule.ID, serverID: snap.ServerID}
	value, state := EvaluateRule(rule, snap, now)

	switch state {
	case AlertUnknown:
		e.mu.Lock()
		delete(e.pending, key)
		e.mu.Unlock()
		return
	case AlertClear:
		e.mu.Lock()
		delete(e.pending, key)
		_, wasFiring := e.firing[key]
		e.mu.Unlock()
		if !wasFiring {
			return
		}
		resolved, err := models.ResolveAlertEvent(e.db, rule.ID, snap.ServerID, now)
		if err != nil {
			// Keep the key in `firing` so the next tick retries. Dropping it here
			// would leave the event open in the database forever, because the
			// engine would no longer see a firing→clear transition to resolve.
			log.Printf("alerts: resolve %s on %s: %v", rule.Name, snap.ServerName, err)
			return
		}
		e.mu.Lock()
		delete(e.firing, key)
		e.mu.Unlock()
		if resolved {
			e.notifier.Send(rule, snap, "resolved", value, now)
		}
		return
	}

	// Breached: start or continue the countdown before opening an event.
	e.mu.Lock()
	if _, already := e.firing[key]; already {
		e.mu.Unlock()
		return
	}
	since, tracked := e.pending[key]
	if !tracked {
		since = now
		e.pending[key] = since
	}
	e.mu.Unlock()

	// "offline" carries its own duration in the staleness check, so it fires as
	// soon as the condition is observed.
	if rule.Metric != models.AlertMetricOffline {
		if now.Sub(since) < time.Duration(rule.Duration)*time.Second {
			return
		}
	}

	message := FormatAlertMessage(rule, snap, value)
	id, err := models.OpenAlertEvent(e.db, &models.AlertEvent{
		RuleID:    rule.ID,
		ServerID:  snap.ServerID,
		UserID:    rule.UserID,
		Value:     value,
		Message:   message,
		StartedAt: now,
	})
	if err != nil {
		log.Printf("alerts: open %s on %s: %v", rule.Name, snap.ServerName, err)
		return
	}
	e.mu.Lock()
	e.firing[key] = struct{}{}
	delete(e.pending, key)
	e.mu.Unlock()
	if id != 0 {
		log.Printf("alerts: firing %q on %s (%s)", rule.Name, snap.ServerName, message)
		e.notifier.Send(rule, snap, "firing", value, now)
	}
}

// releaseStale drops bookkeeping for rule/server pairs the current rule set no
// longer covers — a disabled rule, a narrowed scope, a deleted server — and
// closes their open events. Without this, pausing a rule while it fires would
// leave the alert counted as active indefinitely. Deleting a rule or server
// needs no help here: the events cascade away with the row.
func (e *AlertEngine) releaseStale(live map[alertKey]struct{}, now time.Time) {
	e.mu.Lock()
	for key := range e.pending {
		if _, ok := live[key]; !ok {
			delete(e.pending, key)
		}
	}
	var orphaned []alertKey
	for key := range e.firing {
		if _, ok := live[key]; !ok {
			orphaned = append(orphaned, key)
		}
	}
	e.mu.Unlock()

	for _, key := range orphaned {
		if _, err := models.ResolveAlertEvent(e.db, key.ruleID, key.serverID, now); err != nil {
			// Leave it tracked so the next tick retries the close.
			log.Printf("alerts: release %s/%s: %v", key.ruleID, key.serverID, err)
			continue
		}
		e.mu.Lock()
		delete(e.firing, key)
		e.mu.Unlock()
	}
}

// EvaluateRule samples the rule's metric from a snapshot and classifies it.
// It is pure so the threshold semantics can be unit tested without a database.
func EvaluateRule(rule models.AlertRule, snap models.AlertSnapshot, now time.Time) (float64, AlertState) {
	age := now.Sub(snap.RecordedAt)
	if !snap.HasMetrics {
		age = staleFloor + offlineFloor
	}

	if rule.Metric == models.AlertMetricOffline {
		window := max(time.Duration(rule.Duration)*time.Second, offlineFloor)
		if age >= window {
			return age.Seconds(), AlertBreached
		}
		return age.Seconds(), AlertClear
	}

	// A threshold rule cannot judge a host that stopped reporting. Once the gap
	// exceeds the staleness window, treat the alert as resolved rather than
	// leaving it stuck open forever.
	if age >= staleFloor {
		return 0, AlertClear
	}

	value, ok := sampleMetric(rule.Metric, snap)
	if !ok {
		return 0, AlertUnknown
	}
	if compare(value, rule.Comparator, rule.Threshold) {
		return value, AlertBreached
	}
	return value, AlertClear
}

func sampleMetric(metric string, snap models.AlertSnapshot) (float64, bool) {
	switch metric {
	case models.AlertMetricCPU:
		return snap.CPUPercent, true
	case models.AlertMetricMemory:
		if snap.MemTotal <= 0 {
			return 0, false
		}
		return float64(snap.MemoryUsed) / float64(snap.MemTotal) * 100, true
	case models.AlertMetricDisk:
		if snap.DiskTotal <= 0 {
			return 0, false
		}
		return float64(snap.DiskUsed) / float64(snap.DiskTotal) * 100, true
	case models.AlertMetricLoad1:
		return snap.Load1, true
	case models.AlertMetricLatency:
		// A zero sample means the ping probe produced no answer, not 0 ms.
		if snap.LatencyMS <= 0 {
			return 0, false
		}
		return float64(snap.LatencyMS), true
	}
	return 0, false
}

func compare(value float64, comparator string, threshold float64) bool {
	if comparator == "<" {
		return value < threshold
	}
	return value > threshold
}

// FormatAlertMessage renders the one-line summary stored with the event and
// sent to webhooks when an alert opens.
func FormatAlertMessage(rule models.AlertRule, snap models.AlertSnapshot, value float64) string {
	if rule.Metric == models.AlertMetricOffline {
		base := fmt.Sprintf("%s has not reported for %s", snap.ServerName, formatDuration(time.Duration(value)*time.Second))
		// Whoever reads this at 3am gets the cause with the symptom. "Offline"
		// alone sends them to check the host; "SSH authentication rejected"
		// sends them to the credential, which is where the fix is.
		if reason := OfflineReason(snap.LastErrorKind); reason != "" {
			return base + " — " + reason
		}
		return base
	}
	unit := models.AlertMetricUnits[rule.Metric]
	return fmt.Sprintf("%s %s is %s%s (%s %s%s for %s)",
		snap.ServerName, rule.Metric,
		strconv.FormatFloat(value, 'f', 1, 64), unit,
		rule.Comparator, strconv.FormatFloat(rule.Threshold, 'f', -1, 64), unit,
		formatDuration(time.Duration(rule.Duration)*time.Second))
}

// FormatRecoveryMessage describes the return to normal. Reusing the firing
// wording here would read as "cpu is 9% (> 90%)", which contradicts itself.
func FormatRecoveryMessage(rule models.AlertRule, snap models.AlertSnapshot, value float64) string {
	if rule.Metric == models.AlertMetricOffline {
		return fmt.Sprintf("%s is reporting again", snap.ServerName)
	}
	unit := models.AlertMetricUnits[rule.Metric]
	return fmt.Sprintf("%s %s recovered to %s%s (threshold %s %s%s)",
		snap.ServerName, rule.Metric,
		strconv.FormatFloat(value, 'f', 1, 64), unit,
		rule.Comparator, strconv.FormatFloat(rule.Threshold, 'f', -1, 64), unit)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// ApplyDefaultWebhooks fills in each rule's owner default where the rule
// carries no webhook of its own, in place.
//
// Resolved here rather than in the rule query so the API keeps reporting what
// a rule actually stores: the UI distinguishes "inherits the default" from
// "has its own", and a join would erase that. A rule whose own URL is set
// always wins — the default is a fallback, never an override.
func ApplyDefaultWebhooks(rules []models.AlertRule, defaults map[uuid.UUID]string) {
	if len(defaults) == 0 {
		return
	}
	for i := range rules {
		if strings.TrimSpace(rules[i].WebhookURL) != "" {
			continue
		}
		rules[i].WebhookURL = defaults[rules[i].UserID]
	}
}

// WebhookNotifier delivers alert transitions to a user-configured URL.
type WebhookNotifier struct {
	client       *http.Client
	allowPrivate bool
}

func NewWebhookNotifier(allowPrivate bool) *WebhookNotifier {
	return &WebhookNotifier{
		client: &http.Client{
			Timeout: webhookTimeout,
			// Alerting must not follow redirects: a public URL could otherwise
			// bounce the request into the private network the guard rejects.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		allowPrivate: allowPrivate,
	}
}

type webhookPayload struct {
	Status     string  `json:"status"`
	Rule       string  `json:"rule"`
	Server     string  `json:"server"`
	Metric     string  `json:"metric"`
	Value      float64 `json:"value"`
	Threshold  float64 `json:"threshold"`
	Comparator string  `json:"comparator"`
	Message    string  `json:"message"`
	// Machine-readable cause for an offline alert ("auth", "unreachable", …),
	// so a receiver can route on it instead of matching on the prose in
	// Message. Omitted when there is nothing to report.
	Reason    string    `json:"reason,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Send posts the transition asynchronously; delivery failures are logged and
// never block or fail the evaluation loop.
func (n *WebhookNotifier) Send(rule models.AlertRule, snap models.AlertSnapshot, status string, value float64, at time.Time) {
	target := strings.TrimSpace(rule.WebhookURL)
	if target == "" {
		return
	}
	message := FormatAlertMessage(rule, snap, value)
	if status == "resolved" {
		message = FormatRecoveryMessage(rule, snap, value)
	}
	payload := webhookPayload{
		Status:     status,
		Rule:       rule.Name,
		Server:     snap.ServerName,
		Metric:     rule.Metric,
		Value:      value,
		Threshold:  rule.Threshold,
		Comparator: rule.Comparator,
		Message:    message,
		Timestamp:  at,
	}
	// Only meaningful while the host is actually dark; a recovery carries no
	// cause, and a threshold alert's cause is the threshold.
	if status != "resolved" && rule.Metric == models.AlertMetricOffline {
		payload.Reason = snap.LastErrorKind
	}
	go func() {
		if err := n.Post(context.Background(), target, payload); err != nil {
			log.Printf("alerts: webhook for %q failed: %v", rule.Name, err)
		}
	}()
}

func (n *WebhookNotifier) Post(ctx context.Context, target string, payload any) error {
	if err := n.validateTarget(target); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "server-monitor-alerts/1")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook responded %s", resp.Status)
	}
	return nil
}

// validateTarget rejects anything that is not a plain http(s) URL pointing at a
// routable address. Without this, an authenticated user could use the alerting
// pipeline to probe hosts on the server's private network.
func (n *WebhookNotifier) validateTarget(target string) error {
	parsed, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("webhook URL must use http or https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("webhook URL has no host")
	}
	if n.allowPrivate {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve webhook host: %w", err)
	}
	for _, ip := range ips {
		if isRestrictedIP(ip) {
			return fmt.Errorf("webhook host %s resolves to a non-public address (set ALLOW_PRIVATE_WEBHOOKS=true to permit)", host)
		}
	}
	return nil
}

func isRestrictedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// 100.64.0.0/10 (CGNAT) and 169.254.0.0/16 metadata endpoints are not
	// covered by IsPrivate but are equally unsuitable targets.
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
	}
	return false
}

// ValidateWebhookURL exposes target validation to handlers so a bad URL is
// rejected at save time instead of silently failing on the first alert.
func (n *WebhookNotifier) ValidateWebhookURL(target string) error {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	if len(target) > webhookMaxBodyLength {
		return fmt.Errorf("webhook URL is too long")
	}
	return n.validateTarget(target)
}
