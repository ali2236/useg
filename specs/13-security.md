# 13. Security and privacy

- **No TLS interception.** Only metadata is visible: SNI/Host, destination,
  byte counts, timing. Content stays end to end. This is stated in the UI
  because the operator genuinely can see where the household goes — a real
  privacy fact, not a footnote.
- **Subscription tokens are bearer secrets.** Unguessable, rotatable, and
  revocable per credential.
- **No WAN exposure.** Admin UI, portal, subscription endpoint, proxy inbounds,
  and the Clash API all bind to the LAN or loopback. Clash API is
  `127.0.0.1` only.
- **The Clash API is powerful** (it can close connections and read metadata);
  loopback binding is mandatory, not cosmetic.
- **Admin over plain HTTP on the LAN** is the one weak spot: a LAN attacker can
  sniff the session cookie. Acceptable on a family network; if that is not
  acceptable, reach the admin UI over an SSH tunnel or put it behind a
  self-signed TLS listener. It is deliberately on a separate port (8080) so
  this can change without disturbing the portal.
- **Admin actions are logged**, including weight changes.
- **The household should be told** the system exists and what it records.
- **NTP** matters: a wrong clock makes period boundaries wrong.
