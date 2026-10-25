// PayPal Donations: a PayPal donation button as the [paypal-donation]
// shortcode and a widget (port of the WordPress plugin of the same name).
// With IPN turned on, PayPal reports each donation; the plugin confirms it
// with PayPal, records it (without addresses) and tells the
// administrators. The WordPress plugin's options and the donations of an
// older donation plugin (donation_list posts) are imported on activation.
package main

import gp "github.com/theinvisible/gopress/pdk/go"

func init() {
	gp.HandleShortcode("paypal-donation", shortcode)
	gp.HandleWidget("paypal_donations", widget)
	gp.HandleRoute("/ipn", handleIPN)
	gp.HandleTable("donations", table)
	gp.HandleSubmit("donations", submit)
	gp.HandleDashboard("recent", dashboard)
	gp.HandleMigrate(migrate)
}

func main() {}
