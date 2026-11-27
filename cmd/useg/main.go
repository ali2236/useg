// Command useg is the whole server: lease watcher, accountant, quota engine,
// subscription endpoint, portal and admin UI. sing-box and dnsmasq are separate
// processes; sing-box is supervised from here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"useg/internal/account"
	"useg/internal/config"
	"useg/internal/enforce"
	"useg/internal/jalali"
	"useg/internal/leases"
	"useg/internal/names"
	"useg/internal/quota"
	"useg/internal/singbox"
	"useg/internal/store"
	"useg/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "server":
		err = cmdServer(os.Args[2:])
	case "migrate":
		err = cmdMigrate(os.Args[2:])
	case "seed":
		err = cmdSeed(os.Args[2:])
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "period":
		err = cmdPeriod(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `useg — home gateway quota meter

  useg server  -c /etc/useg/config.yaml    run everything
  useg migrate -c ...                      apply schema migrations
  useg seed    -c ... [-dev]               create demo persons/routes/period
  useg probe   -c ...                      dump one /connections object (P0 check)
  useg period  -c ... [-n 6]               print upcoming Jalali boundaries
`)
	os.Exit(2)
}

func logger() *slog.Logger {
	lvl := slog.LevelInfo
	if os.Getenv("USEG_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("c", "/etc/useg/config.yaml", "config file")
}

func openWithConfig(path string) (*config.Config, *store.DB, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return nil, nil, err
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Migrate(); err != nil {
		return nil, nil, err
	}
	return cfg, db, nil
}

// ---- server -----------------------------------------------------------------

func cmdServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)

	log := logger()
	cfg, db, err := openWithConfig(*cfgPath)
	if err != nil {
		return err
	}
	defer db.Close()
	log.Info("starting", "db", cfg.DBPath, "lan", cfg.LanCIDR, "box", cfg.BoxIP, "enforce", cfg.Enforce)

	// A box set up by make install never runs seed, so give it its routes and
	// listeners here; otherwise its subscription has nothing connectable.
	if err := ensureBase(db); err != nil {
		return err
	}
	if rs, err := db.Routes(); err == nil {
		open := 0
		for _, rt := range rs {
			if rt.Enabled && rt.AllowAll {
				open++
			}
		}
		if open == 0 {
			log.Warn("no route is enabled with allow_all; subscriptions stay empty")
		}
	}

	nft := enforce.New(enforce.Options{
		LanCIDR:    cfg.LanCIDR,
		BoxIP:      cfg.BoxIP,
		RouterIP:   cfg.RouterIP,
		TProxyPort: cfg.TProxyPort,
		PortalPort: cfg.PortalPort,
		Iface:      cfg.Iface,
	}, cfg.Enforce, log)
	bypassSet := false
	if v, err := db.GetSetting("bypass"); err == nil && v == "1" {
		bypassSet = true
		log.Warn("maintenance bypass was left on; not applying the metered path")
	} else if err := nft.Apply(); err != nil {
		log.Error("apply ruleset (running without enforcement)", "err", err)
	}

	clash := singbox.NewClient(cfg.ClashURL)
	acct := account.New(db, clash, log, cfg.LanCIDR, cfg.BoxIP, cfg.Location())

	build := func() ([]byte, error) {
		persons, err := db.Persons()
		if err != nil {
			return nil, err
		}
		routes, err := db.Routes()
		if err != nil {
			return nil, err
		}
		creds, err := db.Credentials()
		if err != nil {
			return nil, err
		}
		return singbox.Build(singbox.Input{
			BoxIP:           cfg.BoxIP,
			TProxyPort:      cfg.TProxyPort,
			ClashController: singbox.ClashController(cfg.ClashURL),
			Persons:         persons,
			Routes:          routes,
			Credentials:     creds,
		})
	}

	var srv *web.Server
	sup := singbox.NewSupervisor(cfg.SingboxBin, cfg.SingboxConfig, build, log, func(reason string) {
		log.Error("data plane unhealthy", "reason", reason)
		_ = nft.Bypass(true)
		_ = db.AddEvent("fail_closed", "gateway", reason)
		if srv != nil {
			srv.SetBypass(true)
		}
	})

	srv, err = web.New(db, cfg, nft, log)
	if err != nil {
		return err
	}
	srv.SetBypass(bypassSet)

	quotaEng := quota.NewEngine(db, nft, cfg, log, acct.Pending)
	srv.SetPending(acct.Pending)

	portal := &http.Server{Addr: cfg.PortalListen, Handler: srv.PortalHandler()}
	admin := &http.Server{Addr: cfg.AdminListen, Handler: srv.AdminHandler()}
	errCh := make(chan error, 3)
	go func() { errCh <- portal.ListenAndServe() }()
	go func() { errCh <- admin.ListenAndServe() }()

	var tlsSrv *http.Server
	if cfg.TLSListen != "" {
		certFile, keyFile := cfg.TLSCert, cfg.TLSKey
		if certFile == "" || keyFile == "" {
			certFile, keyFile, err = web.EnsureCert(filepath.Join(cfg.DataDir, "tls"), cfg.BoxIP)
			if err != nil {
				return fmt.Errorf("tls cert: %w", err)
			}
			log.Info("using self-signed subscription cert", "cert", certFile)
		}
		tlsSrv = &http.Server{Addr: cfg.TLSListen, Handler: srv.PortalHandler()}
		go func() { errCh <- tlsSrv.ListenAndServeTLS(certFile, keyFile) }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	wakeNames := make(chan struct{}, 1)
	go namesLoop(ctx, db, cfg, log, wakeNames)

	periodTick := func(now time.Time) {
		p, err := db.EnsurePeriod(now, cfg.Location())
		if err != nil {
			log.Error("open period", "err", err)
			return
		}
		acct.RollPeriod(p.ID, now)
	}
	periodTick(time.Now())

	sup.Reconcile()

	var lw leaseWatch
	fast := time.NewTicker(time.Duration(cfg.PollMS) * time.Millisecond)
	defer fast.Stop()
	slow := time.NewTicker(time.Second)
	defer slow.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			acct.Close()
			sup.Stop()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = portal.Shutdown(shutdownCtx)
			_ = admin.Shutdown(shutdownCtx)
			if tlsSrv != nil {
				_ = tlsSrv.Shutdown(shutdownCtx)
			}
			return nil
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case now := <-fast.C:
			acct.Tick(now)
		case now := <-slow.C:
			i++
			if i%5 == 0 {
				if err := quotaEng.Tick(now); err != nil {
					log.Error("quota tick", "err", err)
				}
				if joined, err := lw.tick(db, cfg, now); err != nil {
					log.Debug("leases", "err", err)
				} else if joined {
					select {
					case wakeNames <- struct{}{}:
					default:
					}
				}
			}
			if i%10 == 0 {
				sup.Reconcile()
			}
			if i%60 == 0 {
				periodTick(now)
			}
		}
	}
}

// namesLoop fills device.name with what devices advertise over mDNS. Best
// effort: many phones never answer, and their DHCP hostname remains the name.
// It also runs when a lease appears for a device that was not there on the
// previous poll, so a device that just joined gets its name while the admin is
// still looking at the list.
func namesLoop(ctx context.Context, db *store.DB, cfg *config.Config, log *slog.Logger, wake <-chan struct{}) {
	iface, err := net.InterfaceByName(cfg.Iface)
	if err != nil {
		log.Debug("mdns: no interface", "iface", cfg.Iface, "err", err)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	// The first lease poll always looks like a new device; the sweep below
	// already covers it.
	select {
	case <-wake:
	default:
	}
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		found := names.Discover(ctx, iface)
		for ip, n := range found {
			if err := db.SetDeviceName(ip, n); err != nil {
				log.Error("mdns: store name", "ip", ip, "err", err)
			}
		}
		log.Debug("mdns names", "found", len(found))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-wake:
			// Give the fresh device a moment to start answering.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// leaseWatch is the device inventory's only writer on the LAN side. It keeps
// the MAC set of the previous poll so a device that just joined (or whose lease
// just reappeared) can be told apart from one that was already in the file.
type leaseWatch struct {
	seen map[string]bool
}

func (w *leaseWatch) tick(db *store.DB, cfg *config.Config, now time.Time) (bool, error) {
	ls, err := leases.Read(cfg.LeasesFile)
	if err != nil {
		return false, err
	}
	seen := make(map[string]bool, len(ls))
	joined := false
	for _, l := range ls {
		seen[l.MAC] = true
		if !w.seen[l.MAC] {
			joined = true
		}
		if err := db.UpsertLease(l.MAC, l.IP, l.Hostname, now.Unix()); err != nil {
			return false, err
		}
	}
	w.seen = seen
	return joined, nil
}

// ---- migrate / seed ---------------------------------------------------------

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)
	_, db, err := openWithConfig(*cfgPath)
	if err != nil {
		return err
	}
	defer db.Close()
	fmt.Println("migrations applied")
	return nil
}

func cmdSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	cfgPath := configFlag(fs)
	dev := fs.Bool("dev", false, "small quotas so blocking is testable in minutes")
	_ = fs.Parse(args)

	cfg, db, err := openWithConfig(*cfgPath)
	if err != nil {
		return err
	}
	defer db.Close()

	loc := cfg.Location()
	if v, _ := db.GetSetting("anchor_start_jalali"); v == "" || *dev {
		// Anchor on the 12th of the current Jalali month.
		d := jalali.FromTime(time.Now(), loc)
		if err := db.SetAnchor(jalali.Date{Y: d.Y, M: d.M, D: 12}); err != nil {
			return err
		}
	}

	settings := map[string]string{
		"pool_charged_gb": "20",
		"guest_pool_gb":   "2",
		"guest_device_gb": "0.01",
	}
	if *dev {
		settings = map[string]string{
			"pool_charged_gb": "0.05",  // 50 MB charged pool
			"guest_pool_gb":   "0.02",  // 20 MB for guests
			"guest_device_gb": "0.005", // 5 MB per guest device
		}
	}
	for k, v := range settings {
		if err := db.SetSetting(k, v); err != nil {
			return err
		}
	}

	persons, _ := db.Persons()
	if len(persons) == 0 {
		for _, p := range []struct {
			name   string
			weight float64
		}{{"ali", 0.5}, {"sara", 0.3}, {"reza", 0.2}} {
			if _, err := db.CreatePerson(p.name, p.weight, "fa"); err != nil {
				return err
			}
		}
	}

	// One listener per (person, route), so the subscription works immediately.
	if err := ensureBase(db); err != nil {
		return err
	}

	// nimbaha on the primary route costs 0.3: the design's showcase cell.
	var primary, nimbaha int64
	if rs, _ := db.Routes(); len(rs) > 0 {
		primary = rs[0].ID
	}
	if cs, _ := db.Classes(); len(cs) > 0 {
		for _, c := range cs {
			if c.Name == "nimbaha" {
				nimbaha = c.ID
			}
		}
	}
	if primary != 0 && nimbaha != 0 {
		if err := db.SetRate(primary, nimbaha, 0.3); err != nil {
			return err
		}
	}

	if _, err := (&leaseWatch{}).tick(db, cfg, time.Now()); err != nil {
		logger().Debug("no lease file at seed time", "path", cfg.LeasesFile, "err", err)
	}

	period, err := db.EnsurePeriod(time.Now(), loc)
	if err != nil {
		return err
	}
	fmt.Printf("seeded: period %d (%s → %s), %d persons\n", period.ID,
		jalali.Format(time.Unix(period.StartUTC, 0), "long", loc),
		jalali.Format(time.Unix(period.EndUTC, 0), "long", loc), len(mustPersons(db)))
	for _, p := range mustPersons(db) {
		fmt.Printf("  person:%d %-6s sub %s\n", p.ID, p.Name, cfg.SubURL(cfg.BoxIP, p.SubToken))
	}
	return nil
}

func mustPersons(db *store.DB) []store.Person {
	ps, _ := db.Persons()
	return ps
}

// ensureBase gives a box the two things a subscription needs: the default
// direct routes when it has none, and one credential per (person, enabled and
// allow_all route). make install never runs seed, so without this a fresh box
// serves a subscription with nothing connectable and starts no VLESS listener.
// It is idempotent; seed and server both call it.
func ensureBase(db *store.DB) error {
	routes, err := db.Routes()
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		for _, name := range []string{"primary", "upstream"} {
			if _, err := db.SaveRoute(store.Route{
				Name: name, Kind: "direct", UpstreamJSON: "{}",
				AllowAll: true, Enabled: true,
			}); err != nil {
				return err
			}
		}
		if routes, err = db.Routes(); err != nil {
			return err
		}
	}
	persons, err := db.Persons()
	if err != nil {
		return err
	}
	for _, p := range persons {
		for _, rt := range routes {
			if !rt.Enabled || !rt.AllowAll {
				continue
			}
			if _, err := db.EnsureCredential(p.ID, rt.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- probe ------------------------------------------------------------------

// cmdProbe answers the P0 question: which metadata fields this pinned sing-box
// actually exposes. It runs against a live proxy, so it needs traffic first.
func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	cfgPath := configFlag(fs)
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	raw, err := singbox.NewClient(cfg.ClashURL).Probe()
	if err != nil {
		return err
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
	return nil
}

// ---- period -----------------------------------------------------------------

func cmdPeriod(args []string) error {
	fs := flag.NewFlagSet("period", flag.ExitOnError)
	cfgPath := configFlag(fs)
	n := fs.Int("n", 6, "how many boundaries to print")
	_ = fs.Parse(args)
	cfg, db, err := openWithConfig(*cfgPath)
	if err != nil {
		return err
	}
	defer db.Close()
	loc := cfg.Location()
	anchor, err := db.Anchor(loc)
	if err != nil {
		return err
	}
	fmt.Printf("anchor %s (day %d), timezone %s\n", anchor, anchor.D, cfg.Timezone)
	k := 0
	for i := 0; i < *n; i++ {
		b := jalali.Boundary(anchor, k, loc)
		if b.Before(time.Now()) {
			k++
			i--
			continue
		}
		fmt.Printf("  %s  %s\n", jalali.Format(b, "numeric", loc),
			b.UTC().Format(time.RFC3339))
		k++
	}
	return nil
}
