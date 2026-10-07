package names

import "strings"

// DeviceLabel turns a User-Agent header into a short label such as
// "iPhone Safari", "Android Chrome" or "Windows Edge". It returns
// "Unknown device" when nothing useful can be recognised.
func DeviceLabel(ua string) string {
	switch {
	case strings.HasPrefix(ua, "curl/"):
		return "curl"
	case strings.HasPrefix(ua, "Wget/"):
		return "wget"
	}

	var device string
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPod"):
		device = "iPhone"
	case strings.Contains(ua, "iPad"):
		device = "iPad"
	case strings.Contains(ua, "Android"):
		device = "Android"
	case strings.Contains(ua, "CrOS"):
		device = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		device = "Windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		device = "Mac"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		device = "Linux"
	}

	var browser string
	switch {
	case containsAny(ua, "Edg/", "EdgA/", "EdgiOS/"):
		browser = "Edge"
	case containsAny(ua, "OPR/", "OPT/", "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case containsAny(ua, "Firefox/", "FxiOS/"):
		browser = "Firefox"
	case containsAny(ua, "CriOS/", "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}

	switch {
	case device != "" && browser != "":
		return device + " " + browser
	case device != "":
		return device
	case browser != "":
		return browser
	}
	return "Unknown device"
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
