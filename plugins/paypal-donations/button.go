package main

import (
	"html/template"
	"math"
	"strconv"
	"strings"

	gp "github.com/theinvisible/gopress/pdk/go"
)

// options are the values of the settings page.
type options struct {
	Account, Currency, PageStyle, ReturnPage, Purpose, Reference string
	Amount                                                       string // "" or a number > 0
	Button, ButtonURL, ButtonLocalized                           string
	NewTab, Center, DisableStats, Sandbox                        bool
	SetCheckoutLanguage                                          bool
	CheckoutLanguage, ReturnMethod                               string
	IPN, IPNMail                                                 bool
}

func loadOptions() (options, error) {
	m, err := gp.Settings("settings")
	if err != nil {
		return options{}, err
	}
	str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
	flag := func(k string) bool { b, _ := m[k].(bool); return b }
	o := options{Account: str("paypal_account"), Currency: str("currency_code"), PageStyle: str("page_style"),
		ReturnPage: str("return_page"), Purpose: str("purpose"), Reference: str("reference"), Button: str("button"),
		ButtonURL: str("button_url"), ButtonLocalized: str("button_localized"), NewTab: flag("new_tab"),
		Center: flag("center_button"), DisableStats: flag("disable_stats"), Sandbox: flag("sandbox"),
		SetCheckoutLanguage: flag("set_checkout_language"), CheckoutLanguage: str("checkout_language"),
		ReturnMethod: str("return_method"), IPN: flag("ipn_enabled"), IPNMail: flag("ipn_mail")}
	if n, ok := m["amount"].(float64); ok && n > 0 {
		o.Amount = formatAmount(n)
	}
	return o, nil
}

func formatAmount(n float64) string {
	if n == math.Trunc(n) {
		return strconv.FormatFloat(n, 'f', 0, 64)
	}
	return strconv.FormatFloat(n, 'f', 2, 64)
}

// amountAttr checks an amount from a shortcode: a positive number, else
// nothing (the original stopped the whole page instead).
func amountAttr(s string) string {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 || n > 1e6 || math.IsInf(n, 0) || math.IsNaN(n) {
		return ""
	}
	return formatAmount(n)
}

// httpURL accepts http(s) URLs and site paths.
func httpURL(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || (strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//")) {
		return s
	}
	return ""
}

var stockButtons = map[string]string{
	"small": "https://www.paypalobjects.com/en_US/i/btn/btn_donate_SM.gif",
	"large": "https://www.paypalobjects.com/en_US/i/btn/btn_donate_LG.gif",
	"cards": "https://www.paypalobjects.com/en_US/i/btn/btn_donateCC_LG.gif",
}

// hidden is an input of the form.
type hidden struct{ Name, Value string }

// form is the data of templates/form.html.
type form struct {
	Action        string
	NewTab        bool
	Center        bool
	Fields        []hidden
	Button, Pixel string
}

// buildForm builds the donation form like the WordPress plugin, in the
// order of its fields. notify is the IPN URL ("" when IPN is off).
func buildForm(o options, purpose, reference, amount, returnPage, buttonURL, notify string) form {
	f := form{Action: "https://www.paypal.com/cgi-bin/webscr", NewTab: o.NewTab, Center: o.Center}
	if o.Sandbox {
		f.Action = "https://www.sandbox.paypal.com/cgi-bin/webscr"
	}
	add := func(name, value string) { f.Fields = append(f.Fields, hidden{name, value}) }
	add("cmd", "_donations")
	add("bn", "TipsandTricks_SP")
	add("business", o.Account)
	if o.PageStyle != "" {
		add("page_style", o.PageStyle)
	}
	if returnPage = httpURL(returnPage); returnPage == "" {
		returnPage = httpURL(o.ReturnPage)
	}
	if returnPage != "" {
		add("return", returnPage)
	}
	if purpose == "" {
		purpose = o.Purpose
	}
	if purpose != "" {
		add("item_name", purpose)
	}
	if reference == "" {
		reference = o.Reference
	}
	if reference != "" {
		add("item_number", reference)
	}
	if amount = amountAttr(amount); amount == "" {
		amount = o.Amount
	}
	if amount != "" {
		add("amount", amount)
	}
	if notify != "" {
		add("notify_url", notify)
	}
	rm := o.ReturnMethod
	if rm == "" {
		rm = "0"
	}
	add("rm", rm)
	if o.Currency != "" {
		add("currency_code", o.Currency)
	}
	if o.SetCheckoutLanguage && o.CheckoutLanguage != "" {
		add("lc", o.CheckoutLanguage)
	}
	if buttonURL = httpURL(buttonURL); buttonURL == "" {
		buttonURL = httpURL(o.ButtonURL)
	}
	if buttonURL == "" {
		stock, ok := stockButtons[o.Button]
		if !ok {
			stock = stockButtons["large"]
		}
		loc := o.ButtonLocalized
		if loc == "" {
			loc = "en_US"
		}
		buttonURL = strings.Replace(stock, "en_US", loc, 1)
	}
	f.Button = buttonURL
	if !o.DisableStats {
		f.Pixel = "https://www.paypalobjects.com/en_US/i/scr/pixel.gif"
	}
	return f
}

// notifyURL is where PayPal reports donations (IPN), if that is on.
func notifyURL(o options) string {
	if !o.IPN {
		return ""
	}
	site, err := gp.Site()
	if err != nil {
		return ""
	}
	return strings.TrimRight(site.URL, "/") + "/plugins/paypal-donations/ipn"
}

// [paypal-donation purpose reference amount return_page button_url]
func shortcode(r gp.ShortcodeRequest) (gp.Render, error) {
	o, err := loadOptions()
	if err != nil || o.Account == "" {
		return gp.Render{}, err
	}
	f := buildForm(o, r.Attr("purpose", ""), r.Attr("reference", ""), r.Attr("amount", ""), r.Attr("return_page", ""),
		r.Attr("button_url", ""), notifyURL(o))
	out, err := gp.Template("form.html", f)
	return gp.Render{HTML: out}, err
}

// The widget: title, text and the form.
func widget(r gp.WidgetRequest) (gp.Render, error) {
	o, err := loadOptions()
	if err != nil || o.Account == "" {
		return gp.Render{}, err
	}
	str := func(k string) string { s, _ := r.Settings[k].(string); return strings.TrimSpace(s) }
	f := buildForm(o, str("purpose"), str("reference"), "", "", "", notifyURL(o))
	out, err := gp.Template("widget.html", map[string]any{
		"TitleHTML":  template.HTML(r.TitleHTML), // the theme's markup from GoPress
		"Paragraphs": paragraphs(str("text")),
		"Form":       f,
	})
	return gp.Render{HTML: out}, err
}

// paragraphs splits text at blank lines; each paragraph is a list of
// lines (shown with line breaks), like WordPress' wpautop on plain text.
func paragraphs(text string) [][]string {
	var out [][]string
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, p := range strings.Split(text, "\n\n") {
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(p), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			out = append(out, lines)
		}
	}
	return out
}
