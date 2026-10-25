package main

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	gp "github.com/theinvisible/gopress/pdk/go"
)

// IPN (Instant Payment Notification): PayPal posts each payment to
// /plugins/paypal-donations/ipn. A notification counts only after PayPal
// confirmed it (the message is sent back unchanged and PayPal answers
// VERIFIED), it is for this site's account and its transaction is new.

const maxIPNBody = 64 << 10

// ipnTypes are the transaction types recorded as donations.
var ipnTypes = []string{"web_accept", "recurring_payment", "subscr_payment", "send_money", "express_checkout"}

func handleIPN(r gp.Request) gp.Response {
	o, err := loadOptions()
	if err != nil {
		return gp.Response{Status: 503}
	}
	if !o.IPN || o.Account == "" {
		return gp.Response{Status: 404, Body: "not found"}
	}
	if r.Method != "POST" {
		return gp.Response{Status: 405, Body: "method not allowed"}
	}
	if r.Body == "" || len(r.Body) > maxIPNBody {
		return gp.Response{Status: 400}
	}
	fields, err := url.ParseQuery(r.Body)
	if err != nil {
		return gp.Response{Status: 400}
	}
	if fields.Get("test_ipn") == "1" && !o.Sandbox {
		gp.Log("warn", "IPN from the PayPal sandbox ignored (the sandbox is off)")
		return gp.Response{Status: 200}
	}
	host := "ipnpb.paypal.com"
	if o.Sandbox {
		host = "ipnpb.sandbox.paypal.com"
	}
	resp, err := gp.Fetch(gp.HTTPRequest{Method: "POST", URL: "https://" + host + "/cgi-bin/webscr",
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded", "User-Agent": "GoPress-PayPal-Donations/2.0"},
		Body:    "cmd=_notify-validate&" + r.Body})
	if err != nil || resp.Status != 200 {
		// PayPal sends the notification again later.
		gp.Log("warn", "IPN: PayPal could not confirm the notification now")
		return gp.Response{Status: 503}
	}
	switch strings.TrimSpace(resp.Body) {
	case "VERIFIED":
	case "INVALID":
		gp.Log("warn", "IPN: PayPal says the notification is not genuine")
		warnInvalid(o)
		return gp.Response{Status: 200}
	default:
		return gp.Response{Status: 503}
	}
	msg := record(o, decodeFields(fields))
	gp.Log("info", "IPN: "+msg)
	return gp.Response{Status: 200}
}

// record stores a verified notification and returns what happened.
func record(o options, f url.Values) string {
	if !forAccount(o.Account, f) {
		return "for another account, ignored"
	}
	txn := f.Get("txn_id")
	if !reTxn.MatchString(txn) {
		return "no valid transaction id, ignored"
	}
	status := f.Get("payment_status")
	// Refunds and reversals refer to the original transaction.
	if parent := f.Get("parent_txn_id"); parent != "" && reTxn.MatchString(parent) {
		if d, ok, err := loadDonation(parent); err == nil && ok {
			d.Status = status
			saveDonation(parent, d)
			return "donation " + parent + " is now " + status
		}
		return "refers to an unknown transaction, ignored"
	}
	if d, ok, err := loadDonation(txn); err == nil && ok {
		if d.Status == status {
			return "already recorded"
		}
		d.Status = status
		saveDonation(txn, d)
		return "donation " + txn + " is now " + status
	}
	if status != "Completed" {
		return "status " + status + ", not recorded yet"
	}
	if !slices.Contains(ipnTypes, f.Get("txn_type")) {
		return "transaction type " + f.Get("txn_type") + ", ignored"
	}
	d := Donation{TxnID: txn, Amount: f.Get("mc_gross"), Fee: f.Get("mc_fee"), Currency: f.Get("mc_currency"),
		Status: status, Type: f.Get("txn_type"), FirstName: f.Get("first_name"), LastName: f.Get("last_name"),
		Email: f.Get("payer_email"), Purpose: f.Get("item_name"), Reference: f.Get("item_number"), Memo: f.Get("memo"),
		Verified: true, Source: "ipn", Received: time.Now().UTC().Format(time.RFC3339)}
	if t, ok := paymentDate(f.Get("payment_date")); ok {
		d.Date = t
	} else {
		d.Date = d.Received
	}
	if err := saveDonation(txn, d); err != nil {
		return "storing failed: " + err.Error()
	}
	text := "New donation: " + d.Money()
	if d.Name() != "" {
		text += " from " + d.Name()
	}
	gp.NotifyAdmins(text, "success", "manage_options")
	if o.IPNMail {
		gp.MailAdmins(text, donationMail(d))
	}
	return "donation " + txn + " recorded"
}

func donationMail(d Donation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A donation was made and confirmed by PayPal.\n\nAmount: %s\n", d.Money())
	for _, l := range [][2]string{{"Name", d.Name()}, {"Email", d.Email}, {"Purpose", d.Purpose}, {"Reference", d.Reference},
		{"Message", d.Memo}, {"Date", d.Date}, {"Transaction", d.TxnID}} {
		if l[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", l[0], l[1])
		}
	}
	b.WriteString("\nAll donations are listed in the admin under Donations.")
	return b.String()
}

// forAccount: the payment went to the site's PayPal account (its email or
// merchant id).
func forAccount(account string, f url.Values) bool {
	account = strings.TrimSpace(account)
	for _, k := range []string{"receiver_email", "business", "receiver_id"} {
		if v := strings.TrimSpace(f.Get(k)); v != "" && strings.EqualFold(v, account) {
			return true
		}
	}
	return false
}

// warnInvalid tells the administrators about a forged or broken
// notification, at most once an hour.
func warnInvalid(o options) {
	var last string
	gp.KVGet("meta", "invalid_warned", &last)
	if t, err := time.Parse(time.RFC3339, last); err == nil && time.Since(t) < time.Hour {
		return
	}
	gp.KVSet("meta", "invalid_warned", time.Now().UTC().Format(time.RFC3339))
	text := "PayPal did not confirm a payment notification sent to this site. Someone may be sending fake notifications; nothing was recorded."
	gp.NotifyAdmins(text, "warning", "manage_options")
	if o.IPNMail {
		gp.MailAdmins("Unconfirmed payment notification", text)
	}
}

// decodeFields converts the values from the notification's charset
// (PayPal's default is windows-1252) to UTF-8.
func decodeFields(f url.Values) url.Values {
	cs := strings.ToLower(f.Get("charset"))
	if cs == "" || cs == "utf-8" || cs == "utf8" {
		return f
	}
	if cs != "windows-1252" && cs != "iso-8859-1" && cs != "latin1" {
		return f
	}
	out := url.Values{}
	for k, vs := range f {
		for _, v := range vs {
			out.Add(k, latin(v, cs == "windows-1252"))
		}
	}
	return out
}

// cp1252 are the characters of windows-1252 at 0x80–0x9f.
var cp1252 = [32]rune{'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8d, 'Ž', 0x8f,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9d, 'ž', 'Ÿ'}

func latin(s string, win bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c < 0x80:
			b.WriteByte(c)
		case c < 0xa0 && win:
			b.WriteRune(cp1252[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}
