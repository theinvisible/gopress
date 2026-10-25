package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	gp "github.com/theinvisible/gopress/pdk/go"
)

// Donation is a recorded donation (KV donations/<key>). Only what the
// donation list needs is kept: no addresses, payer ids or raw messages.
type Donation struct {
	TxnID     string `json:"txn_id"`
	Date      string `json:"date"` // RFC 3339, UTC
	Amount    string `json:"amount"`
	Fee       string `json:"fee,omitempty"`
	Currency  string `json:"currency"`
	Status    string `json:"status"` // PayPal's payment_status (Completed, Refunded …)
	Type      string `json:"type"`   // txn_type (web_accept, recurring_payment …)
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Email     string `json:"email,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
	Reference string `json:"reference,omitempty"`
	Memo      string `json:"memo,omitempty"`
	Verified  bool   `json:"verified"` // confirmed by PayPal
	Source    string `json:"source"`   // "ipn" or "wordpress"
	Received  string `json:"received,omitempty"`
}

const collDonations = "donations"

var reTxn = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Name is the donor's name.
func (d Donation) Name() string { return strings.TrimSpace(d.FirstName + " " + d.LastName) }

// Money is the amount with its currency.
func (d Donation) Money() string { return strings.TrimSpace(d.Amount + " " + d.Currency) }

func loadDonation(key string) (Donation, bool, error) {
	var d Donation
	ok, err := gp.KVGet(collDonations, key, &d)
	return d, ok, err
}

func saveDonation(key string, d Donation) error { return gp.KVSet(collDonations, key, d) }

// allDonations returns every donation, newest first.
func allDonations() ([]Donation, []string, error) {
	type entry struct {
		key string
		d   Donation
	}
	var list []entry
	after := ""
	for {
		entries, next, err := gp.KVList(collDonations, after, 500)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range entries {
			var d Donation
			if json.Unmarshal(e.Value, &d) == nil {
				list = append(list, entry{e.Key, d})
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	slices.SortStableFunc(list, func(a, b entry) int {
		if c := strings.Compare(b.d.Date, a.d.Date); c != 0 {
			return c
		}
		return strings.Compare(b.key, a.key)
	})
	out := make([]Donation, len(list))
	keys := make([]string, len(list))
	for i, e := range list {
		out[i], keys[i] = e.d, e.key
	}
	return out, keys, nil
}

const perPage = 50

// The donation list (admin table page).
func table(r gp.AdminRequest) (gp.Table, error) {
	all, keys, err := allDonations()
	if err != nil {
		return gp.Table{}, err
	}
	q := strings.ToLower(strings.TrimSpace(r.Query["s"]))
	var rows []gp.Row
	for i, d := range all {
		if q != "" && !strings.Contains(strings.ToLower(strings.Join([]string{d.Name(), d.Email, d.Purpose, d.Reference, d.Memo, d.TxnID}, " ")), q) {
			continue
		}
		status := d.Status
		if !d.Verified {
			status += " (not verified)"
		}
		purpose := d.Purpose
		if d.Memo != "" {
			purpose = strings.TrimSpace(purpose + " – " + d.Memo)
		}
		rows = append(rows, gp.Row{ID: keys[i], Cells: map[string]string{"date": dateOnly(d.Date), "name": d.Name(),
			"email": d.Email, "amount": d.Money(), "purpose": purpose, "type": typeLabel(d.Type), "status": status}})
	}
	total := len(rows)
	page, _ := strconv.Atoi(r.Query["paged"])
	page = max(page, 1)
	start := min((page-1)*perPage, total)
	end := min(start+perPage, total)
	return gp.Table{Rows: rows[start:end], Total: total, PerPage: perPage,
		Actions: []gp.Option{{Value: "delete", Label: "Delete permanently"}}}, nil
}

func submit(r gp.AdminRequest) (string, error) {
	if r.Action != "delete" {
		return "", fmt.Errorf("unknown action")
	}
	n := 0
	for _, id := range r.IDs {
		if _, ok, err := loadDonation(id); err != nil || !ok {
			continue
		}
		if err := gp.KVDelete(collDonations, id); err != nil {
			return "", err
		}
		n++
	}
	if n == 1 {
		return "1 donation deleted.", nil
	}
	return fmt.Sprintf("%d donations deleted.", n), nil
}

func dateOnly(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return s
}

func typeLabel(t string) string {
	switch t {
	case "web_accept", "":
		return "Donation"
	case "recurring_payment", "subscr_payment":
		return "Recurring"
	case "send_money":
		return "Money sent"
	}
	return t
}

// The dashboard box: the sum of the last 12 months per currency and the
// latest donations.
func dashboard(gp.DashboardRequest) (gp.Render, error) {
	all, _, err := allDonations()
	if err != nil {
		return gp.Render{}, err
	}
	o, err := loadOptions()
	if err != nil {
		return gp.Render{}, err
	}
	since := time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339)
	sums := map[string]float64{}
	type recent struct{ Date, Name, Amount string }
	var latest []recent
	for _, d := range all {
		if d.Status != "Completed" || !d.Verified {
			continue
		}
		if len(latest) < 5 {
			latest = append(latest, recent{dateOnly(d.Date), d.Name(), d.Money()})
		}
		if d.Date >= since {
			if n, err := strconv.ParseFloat(d.Amount, 64); err == nil {
				sums[d.Currency] += n
			}
		}
	}
	var totals []string
	for c, n := range sums {
		totals = append(totals, strconv.FormatFloat(n, 'f', 2, 64)+" "+c)
	}
	slices.Sort(totals)
	out, err := gp.Template("dashboard.html", map[string]any{"Totals": totals, "Recent": latest, "IPN": o.IPN})
	return gp.Render{HTML: out}, err
}

// The WordPress import: the plugin's options and the donations an older
// donation plugin stored as donation_list posts.
func migrate() (string, error) {
	var notes []string
	opts, err := gp.Stash("option")
	if err != nil {
		return "", err
	}
	if raw, ok := opts["paypal_donations_options"]; ok {
		skipped, err := importOptions(raw)
		if err != nil {
			return "", err
		}
		notes = append(notes, "Settings imported.")
		if len(skipped) > 0 {
			notes = append(notes, "Not imported (invalid): "+strings.Join(skipped, ", ")+".")
		}
	}
	posts, err := gp.Stash("post:donation_list")
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(posts))
	for k := range posts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	n := 0
	for i, k := range keys {
		key, d, ok := fromStash(posts[k])
		if !ok {
			continue
		}
		if err := saveDonation(key, d); err != nil {
			return "", err
		}
		n++
		if i%10 == 0 {
			gp.Progress(i*100/len(keys), "Donations")
		}
	}
	if len(keys) > 0 {
		gp.Progress(100, "Donations")
		notes = append(notes, fmt.Sprintf("%d donations imported.", n))
	}
	gp.Invalidate()
	return strings.Join(notes, " "), nil
}

// optionFields maps the WordPress option's keys to settings fields.
var optionFields = []string{"paypal_account", "currency_code", "page_style", "return_page", "amount", "purpose", "reference",
	"button", "button_url", "button_localized", "new_tab", "center_button", "disable_stats", "sandbox",
	"set_checkout_language", "checkout_language", "return_method"}

var checkboxes = map[string]bool{"new_tab": true, "center_button": true, "disable_stats": true, "sandbox": true,
	"set_checkout_language": true}

// importOptions sets the settings from paypal_donations_options; values
// the settings page refuses are left at their defaults and reported.
func importOptions(raw json.RawMessage) ([]string, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil // not an array: nothing to import
	}
	values := map[string]any{}
	for _, k := range optionFields {
		v, ok := m[k]
		switch {
		case checkboxes[k]:
			// WordPress keeps checked boxes only.
			values[k] = ok && v != nil && fmt.Sprint(v) != "" && fmt.Sprint(v) != "0"
		case ok && v != nil:
			s := strings.TrimSpace(fmt.Sprint(v))
			if k == "amount" {
				n, err := strconv.ParseFloat(s, 64)
				if err != nil {
					continue
				}
				values[k] = n
				continue
			}
			values[k] = s
		}
	}
	if gp.SetSettings("settings", values) == nil {
		return nil, nil
	}
	// One by one: keep what is valid.
	var skipped []string
	for _, k := range optionFields {
		v, ok := values[k]
		if !ok {
			continue
		}
		if gp.SetSettings("settings", map[string]any{k: v}) != nil {
			skipped = append(skipped, k)
		}
	}
	return skipped, nil
}

// fromStash converts a donation_list post (the IPN fields as meta).
func fromStash(raw json.RawMessage) (string, Donation, bool) {
	var p struct {
		ID      int64          `json:"id"`
		Title   string         `json:"title"`
		DateGMT string         `json:"date_gmt"`
		Meta    map[string]any `json:"meta"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ID <= 0 {
		return "", Donation{}, false
	}
	meta := func(k string) string {
		v, ok := p.Meta[k]
		if !ok || v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
	d := Donation{TxnID: meta("txn_id"), Amount: meta("mc_gross"), Fee: meta("mc_fee"), Currency: meta("mc_currency"),
		Status: meta("payment_status"), Type: meta("txn_type"), FirstName: meta("first_name"), LastName: meta("last_name"),
		Email: meta("payer_email"), Purpose: meta("item_name"), Reference: meta("item_number"), Memo: meta("memo"),
		Verified: strings.EqualFold(meta("IPN_status"), "verified"), Source: "wordpress"}
	if d.Amount == "" {
		d.Amount = meta("amount")
	}
	if d.TxnID == "" {
		d.TxnID = strings.TrimSpace(p.Title)
	}
	if t, ok := paymentDate(meta("payment_date")); ok {
		d.Date = t
	} else if t, err := time.Parse("2006-01-02 15:04:05", p.DateGMT); err == nil {
		d.Date = t.UTC().Format(time.RFC3339)
	}
	key := d.TxnID
	if !reTxn.MatchString(key) {
		key = "wp-" + strconv.FormatInt(p.ID, 10)
	}
	return key, d, true
}

// paymentDate parses PayPal's payment_date ("10:23:45 Jan 02, 2026 PST").
func paymentDate(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	i := strings.LastIndexByte(s, ' ')
	if i < 0 {
		return "", false
	}
	offset := map[string]int{"PST": -8, "PDT": -7, "UTC": 0, "GMT": 0}[s[i+1:]]
	t, err := time.Parse("15:04:05 Jan 2, 2006", s[:i])
	if err != nil {
		return "", false
	}
	return t.Add(-time.Duration(offset) * time.Hour).UTC().Format(time.RFC3339), true
}
