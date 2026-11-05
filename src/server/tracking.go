package server

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/apimgr/cairnbin/src/config"
)

// renderTrackingScript returns the analytics platform embed snippet for the
// configured tracking type, or empty when tracking is disabled (PART 31).
// The returned markup is placed inside a consent-gated <template> element and
// only activated client-side after the visitor's analytics consent is known.
func renderTrackingScript(t config.TrackingConfig) template.HTML {
	if !t.Enabled() {
		return ""
	}

	switch t.Type {
	case "google":
		if strings.HasPrefix(t.ID, "G-") {
			return template.HTML(fmt.Sprintf(`
<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>
  window.dataLayer = window.dataLayer || [];
  function gtag(){dataLayer.push(arguments);}
  gtag('js', new Date());
  gtag('config', '%s');
</script>`, t.ID, t.ID))
		}
		return template.HTML(fmt.Sprintf(`
<script async src="https://www.google-analytics.com/analytics.js"></script>
<script>
  window.ga=window.ga||function(){(ga.q=ga.q||[]).push(arguments)};ga.l=+new Date;
  ga('create', '%s', 'auto');
  ga('send', 'pageview');
</script>`, t.ID))

	case "matomo", "piwik":
		return template.HTML(fmt.Sprintf(`
<script>
  var _paq = window._paq = window._paq || [];
  _paq.push(['trackPageView']);
  _paq.push(['enableLinkTracking']);
  (function() {
    var u="%s/";
    _paq.push(['setTrackerUrl', u+'matomo.php']);
    _paq.push(['setSiteId', '%s']);
    var d=document, g=d.createElement('script'), s=d.getElementsByTagName('script')[0];
    g.async=true; g.src=u+'matomo.js'; s.parentNode.insertBefore(g,s);
  })();
</script>`, strings.TrimSuffix(t.URL, "/"), t.ID))

	case "owa":
		return template.HTML(fmt.Sprintf(`
<script>
  var owa_baseUrl = '%s/';
  var owa_cmds = owa_cmds || [];
  owa_cmds.push(['setSiteId', '%s']);
  owa_cmds.push(['trackPageView']);
  owa_cmds.push(['trackClicks']);
  (function() {
    var _owa = document.createElement('script'); _owa.type = 'text/javascript'; _owa.async = true;
    _owa.src = owa_baseUrl + 'modules/base/js/owa.tracker-combined-min.js';
    var _owa_s = document.getElementsByTagName('script')[0]; _owa_s.parentNode.insertBefore(_owa, _owa_s);
  })();
</script>`, strings.TrimSuffix(t.URL, "/"), t.ID))

	case "fathom":
		scriptURL := "https://cdn.usefathom.com/script.js"
		if t.URL != "" {
			scriptURL = strings.TrimSuffix(t.URL, "/") + "/tracker.js"
		}
		return template.HTML(fmt.Sprintf(`
<script src="%s" data-site="%s" defer></script>`, scriptURL, t.ID))

	case "plausible":
		scriptURL := "https://plausible.io/js/script.js"
		if t.URL != "" {
			scriptURL = strings.TrimSuffix(t.URL, "/") + "/js/script.js"
		}
		return template.HTML(fmt.Sprintf(`
<script defer data-domain="%s" src="%s"></script>`, t.ID, scriptURL))

	case "umami":
		return template.HTML(fmt.Sprintf(`
<script defer src="%s/script.js" data-website-id="%s"></script>`,
			strings.TrimSuffix(t.URL, "/"), t.ID))

	case "simple":
		return template.HTML(`
<script async defer src="https://scripts.simpleanalyticscdn.com/latest.js"></script>
<noscript><img src="https://queue.simpleanalyticscdn.com/noscript.gif" alt="" referrerpolicy="no-referrer-when-downgrade" /></noscript>`)

	case "cloudflare":
		return template.HTML(fmt.Sprintf(`
<script defer src='https://static.cloudflareinsights.com/beacon.min.js' data-cf-beacon='{"token": "%s"}'></script>`, t.ID))

	default:
		return ""
	}
}
