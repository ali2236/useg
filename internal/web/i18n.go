package web

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"useg/internal/jalali"
)

type lang struct {
	code string
	dir  string
}

func pickLang(r string) lang {
	if strings.HasPrefix(strings.ToLower(r), "en") {
		return lang{"en", "ltr"}
	}
	return lang{"fa", "rtl"}
}

var messages = map[string][2]string{
	"portal.title":     {"دستگاه من", "My device"},
	"portal.hello":     {"سلام", "Hello"},
	"portal.online":    {"متصل", "Connected"},
	"portal.offline":   {"قطع", "Disconnected"},
	"portal.device":    {"دستگاه", "Device"},
	"portal.person":    {"کاربر", "Person"},
	"portal.guest":     {"مهمان", "Guest"},
	"portal.used":      {"مصرف‌شده", "Used"},
	"portal.remaining": {"باقی‌مانده", "Remaining"},
	"portal.of":        {"از", "of"},
	"portal.unlimited": {"نامحدود", "Unlimited"},
	"portal.blocked":   {"اینترنت این دستگاه قطع شده است", "Internet access is blocked for this device"},
	"portal.blocked_hint": {"برای وصل شدن دوباره با مدیر خانه صحبت کنید.",
		"Ask the household admin to restore access."},
	"portal.blocked_short": {"قطع", "Blocked"},
	"portal.devices":       {"دستگاه‌ها", "Devices"},
	"portal.this_device":   {"این دستگاه", "This device"},
	"portal.days_left":     {"روز باقی‌مانده", "days left"},
	"portal.you":           {"شما", "You"},
	"portal.period_ends":   {"پایان دوره", "Period ends"},
	"portal.unknown":       {"این دستگاه در شبکه شناسایی نشد.", "This device was not recognised on the LAN."},
	"portal.free":          {"باقی‌مانده", "left"},
	"portal.transfer":      {"انتقال داده", "Transfer data"},
	"portal.bound":         {"این دستگاه به این کاربر وصل است.", "This device is bound to this person."},
	"transfer.title":       {"انتقال داده", "Transfer data"},
	"transfer.from":        {"از", "From"},
	"transfer.to":          {"به", "To"},
	"transfer.amount":      {"مقدار (گیگابایت)", "Amount (GB)"},
	"transfer.available":   {"باقی‌مانده شما", "Your remaining"},
	"transfer.submit":      {"انتقال", "Transfer"},
	"transfer.back":        {"بازگشت", "Back"},
	"transfer.ok":          {"داده منتقل شد", "Data transferred"},
	"transfer.err_amount":  {"مقدار معتبر وارد کنید", "Enter a valid amount"},
	"transfer.err_insufficient": {"باقی‌مانده کافی ندارید",
		"Not enough data left"},
	"transfer.err_failed": {"انتقال انجام نشد", "Transfer failed"},

	"sub.remaining":     {"باقی‌مانده: %s از %s", "Remaining: %s of %s"},
	"sub.unlimited":     {"باقی‌مانده: نامحدود", "Remaining: unmetered"},
	"reason.quota":      {"سهم ماهانه این کاربر تمام شده است", "This person's monthly share is used up"},
	"reason.guest":      {"سهم مهمان این دستگاه تمام شده است", "This device's guest allowance is used up"},
	"reason.guest_pool": {"سهم مهمان‌های این دوره تمام شده است", "The guest pool for this period is used up"},
	"reason.household":  {"سقف مصرف کل خانه رسیده است", "The household's raw ceiling was reached"},
	"reason.manual":     {"این دستگاه توسط مدیر قطع شده است", "An admin blocked this device"},

	"login.password": {"گذرواژه", "password"},
	"login.signin":   {"ورود", "sign in"},
	"login.wrong":    {"گذرواژه نادرست است", "wrong password"},
	"login.nopass":   {"admin_password تنظیم نشده است", "no admin_password configured"},

	"saved":           {"ذخیره شد", "saved"},
	"admin.period":    {"دوره", "Period"},
	"admin.bypass_on": {"بایپس روشن", "BYPASS ON"},
	"admin.metering":  {"اندازه‌گیری فعال", "metering on"},
	"nav.dashboard":   {"داشبورد", "Dashboard"},
	"nav.data":        {"داده", "Data"},
	"nav.persons":     {"افراد", "People"},
	"nav.devices":     {"دستگاه‌ها", "Devices"},
	"nav.domestic":    {"نیم‌بها", "Domestic"},
	"nav.routes":      {"مسیرها", "Routes"},
	"nav.usage":       {"مصرف", "Usage"},
	"nav.audit":       {"رویدادها", "Audit"},
	"nav.machine":     {"ماشین", "Machine"},
	"nav.logout":      {"خروج", "Log out"},
	"bypass.title":    {"بایپس روشن است.", "Bypass is on."},
	"bypass.body":     {"TPROXY متوقف است و هیچ ترافیکی اندازه‌گیری یا مسدود نمی‌شود.", "TPROXY is stopped and nothing is metered or blocked."},
	"bypass.restore":  {"بازگرداندن اندازه‌گیری", "Restore metering"},

	"caps.badge":  {"سقف‌ها خاموش", "CAPS OFF"},
	"caps.title":  {"حذف همه سقف‌ها", "Disable all caps"},
	"caps.hint":   {"همه اینترنت نامحدود می‌گیرند؛ مصرف همچنان اندازه‌گیری و نمایش داده می‌شود. قطع دستی دستگاه‌ها باقی می‌ماند.", "Everyone gets unlimited internet; usage is still measured and shown. Manual device blocks still apply."},
	"caps.toggle": {"سقف‌ها خاموش باشند", "Caps off"},
	"caps.save":   {"ذخیره", "save"},
	"caps.off":    {"سقف‌ها خاموش است: هیچ کاربری به خاطر سهمیه مسدود نمی‌شود.", "Caps are off: nobody is blocked for quota."},

	"household.charged_pool":  {"مصرف‌شده / سهمیه", "Charged / pool"},
	"household.of":            {"از", "of"},
	"household.unmetered":     {"بدون سهمیه", "unmetered"},
	"household.guest_pool":    {"استخر مهمان", "Guest pool used"},
	"household.raw":           {"ترافیک خام (سمت ISP)", "Raw bytes (ISP side)"},
	"household.ceiling":       {"سقف", "ceiling"},
	"household.not_set":       {"تعیین نشده", "not set"},
	"household.ledger_note":   {"دفتر مصرف می‌تواند از قبض بیشتر شود؛ این خط محافظ است", "the ledger can exceed the bill, this is the guardrail"},
	"household.drift":         {"انحراف", "drift"},
	"household.ledger":        {"دفتر مصرف", "ledger"},
	"household.drift_unavail": {"انحراف: شمارنده‌های nft در دسترس نیست", "drift: nft counters unavailable"},
	"household.counts":        {"دستگاه‌ها / افراد", "Devices / persons"},
	"household.blocked_now":   {"در حال قطع", "blocked now"},

	"dash.people":      {"مصرف افراد", "People usage"},
	"dash.people_hint": {"مصرف هر فرد شامل دستگاه‌های متصل به اوست.", "Each person's usage includes the devices bound to them."},
	"dash.connected":   {"دستگاه‌های متصل", "Connected devices"},
	"dash.guests":      {"دستگاه‌های مهمان", "Guest devices"},
	"dash.devices":     {"دستگاه", "devices"},
	"dash.guest_pool":  {"استخر مهمان", "Guest pool"},
	"dash.none":        {"موردی نیست", "none"},

	"th.name":         {"نام", "name"},
	"th.cap":          {"سهمیه", "cap"},
	"th.used":         {"مصرف", "used"},
	"th.remaining":    {"باقی‌مانده", "remaining"},
	"th.subscription": {"اشتراک", "subscription"},
	"th.device":       {"دستگاه", "device"},
	"th.ip":           {"آی‌پی", "ip"},
	"th.owner":        {"مالک", "owner"},
	"th.usage":        {"مصرف", "usage"},
	"th.state":        {"وضعیت", "state"},
	"th.last_used":    {"آخرین استفاده", "last used"},
	"th.kind":         {"نوع", "kind"},
	"th.access":       {"دسترسی", "access"},
	"th.upstream":     {"خروجی JSON", "upstream json"},
	"th.route":        {"مسیر", "route"},
	"th.subject":      {"موضوع", "subject"},
	"th.class":        {"دسته", "class"},
	"th.up":           {"ارسال", "up"},
	"th.down":         {"دریافت", "down"},
	"th.charged":      {"محاسبه‌شده", "charged"},
	"th.when":         {"زمان", "when"},
	"th.detail":       {"جزئیات", "detail"},
	"th.domain":       {"دامنه", "domain"},
	"th.network":      {"شبکه IP", "IP network"},
	"th.updated":      {"بروزرسانی", "updated"},
	"th.due":          {"مهلت", "due"},

	"persons.title":  {"افراد", "People"},
	"persons.new_ph": {"فرد جدید", "new person"},
	"persons.add":    {"افزودن فرد", "add person"},
	"persons.sub":    {"لینک اشتراک", "sub URL"},

	"people.total":       {"حجم کل دوره", "Cycle allowance"},
	"people.days_left":   {"روز تا پایان دوره", "Days left"},
	"people.alloc_title": {"تخصیص حجم بین افراد", "Allocation by person"},
	"people.alloc_hint":  {"سهم هر فرد از حجم دوره و بخش تخصیص‌نیافته.", "Each person's share of the cycle allowance and what is unallocated."},
	"people.unallocated": {"تخصیص‌نیافته", "Unallocated"},
	"people.usage_title": {"مصرف هر فرد", "Usage by person"},
	"people.usage_hint":  {"مصرف هر فرد از سهم خود و بخش استفاده‌نشده.", "Each person's usage of their share and what is left unused."},
	"people.unused":      {"استفاده‌نشده", "Unused"},
	"people.pool_unset":  {"سهمیه دوره تعیین نشده است؛ برای دیدن نمودارها در تنظیمات دوره حجم دوره را وارد کنید.", "No cycle allowance is set; enter one in Period settings to see the charts."},

	"data.title":            {"چرخه داده", "Data cycle"},
	"data.pool":             {"حجم دوره", "Cycle data"},
	"data.pool_hint":        {"کل حجم این چرخه", "Total for this cycle"},
	"data.allocated":        {"تخصیص‌یافته", "Allocated"},
	"data.people":           {"افراد", "people"},
	"data.guests":           {"مهمان‌ها", "Guests"},
	"data.unallocated":      {"تخصیص‌نیافته", "Unallocated"},
	"data.unallocated_hint": {"آماده تخصیص", "ready to allocate"},
	"data.used":             {"مصرف‌شده", "Used"},
	"data.over":             {"بیش از حجم دوره", "over the cycle data"},
	"data.nothing_allocated": {"هنوز حجمی تخصیص نیافته است؛ تا تخصیص ندهید همه افراد و مهمان‌ها قطع می‌مانند.",
		"Nothing is allocated yet; everyone stays blocked until you allocate."},
	"data.monthly_hint": {"دوره فعلی ماهانه است. برای استفاده از چرخه داده، یک چرخه تازه بسازید.",
		"The current period is monthly. Start a data cycle below to use explicit allocations."},
	"data.reset": {"شروع چرخه تازه", "Start a new cycle"},
	"data.reset_hint": {"دوره فعلی بسته می‌شود و حجم تازه، تخصیص‌نیافته آغاز می‌شود.",
		"Closes the current cycle and starts with all new data unallocated."},
	"data.length":      {"طول چرخه", "Cycle length"},
	"data.unit":        {"واحد", "Unit"},
	"data.unit.days":   {"روز", "days"},
	"data.unit.months": {"ماه", "months"},
	"data.data":        {"حجم (GB)", "Data (GB)"},
	"data.reset_btn":   {"شروع چرخه", "Start cycle"},
	"data.reset_warn": {"مصرف قبلی در دوره خودش می‌ماند. تا وقتی حجم تخصیص ندهید، افراد و مهمان‌ها داده‌ای ندارند.",
		"Old usage stays in its own cycle. Until you allocate, people and guests have no data."},
	"data.add":        {"افزودن / کاهش حجم", "Add / remove data"},
	"data.add_hint":   {"افزودن به بخش تخصیص‌نیافته؛ کاهش فقط از بخش تخصیص‌نیافته برمی‌دارد.", "Adds to the unallocated pool; remove takes only from the unallocated remainder."},
	"data.add_btn":    {"افزودن", "Add"},
	"data.remove_btn": {"کاهش", "Remove"},
	"data.removed_ok": {"حجم از تخصیص‌نیافته کم شد", "removed from unallocated"},
	"data.sync":       {"همگام‌سازی با ISP", "Sync with ISP"},
	"data.sync_hint": {"باقی‌مانده داده را از صفحه ISP وارد کنید؛ اختلاف با دفتر مصرف به بخش تخصیص‌نیافته اضافه می‌شود.",
		"Enter the remaining data from the ISP page; the difference with the ledger is added to the unallocated pool."},
	"data.sync_remaining": {"باقی‌مانده ISP (GB)", "ISP remaining (GB)"},
	"data.sync_btn":       {"همگام‌سازی", "Sync"},
	"data.synced_ok":      {"همگام‌سازی انجام شد", "synced"},
	"data.equal":          {"تقسیم مساوی", "Allocate equally"},
	"data.equal_hint": {"باقی‌مانده تخصیص‌نیافته به‌طور مساوی بین افراد و مهمان‌ها تقسیم می‌شود.",
		"Splits the unallocated remainder equally across people and guests."},
	"data.equal_btn": {"تقسیم مساوی", "Split equally"},
	"data.weight":    {"تقسیم وزنی", "Allocate by weight"},
	"data.weight_hint": {"وزن صفر یعنی بدون سهم. فقط بخش تخصیص‌نیافته تقسیم می‌شود.",
		"Weight 0 means no share. Only the unallocated remainder is split."},
	"data.weight_col": {"وزن", "Weight"},
	"data.weight_btn": {"تخصیص وزنی", "Allocate"},
	"data.adjust_col": {"افزودن/کاهش حجم (GB)", "Add / remove (GB)"},
	"data.adjust_hint": {"+ حجم را از بخش تخصیص‌نیافته به این مورد می‌دهد؛ − فقط حجم استفاده‌نشده را به آن برمی‌گرداند.",
		"+ takes GB from the unallocated pool; − returns only the unused part to it."},
	"data.allocate":       {"تخصیص", "Allocate"},
	"data.unallocate":     {"آزادسازی", "Unallocate"},
	"data.unallocated_ok": {"حجم آزاد شد", "unallocated"},
	"data.settings":       {"تنظیمات داده", "Data settings"},
	"data.settings_hint":  {"سهمیه هر مهمان همیشه اعمال می‌شود؛ حجم دوره فقط در دوره ماهانه.", "The per-guest cap always applies; the pool only in monthly periods."},
	"data.reset_ok":       {"چرخه تازه شروع شد", "new cycle started"},
	"data.added_ok":       {"حجم اضافه شد", "data added"},
	"data.allocated_ok":   {"تخصیص انجام شد", "allocation done"},

	"devices.title":   {"دستگاه‌ها", "Devices"},
	"devices.guest":   {"مهمان", "guest"},
	"devices.bind":    {"اتصال", "bind"},
	"devices.unbind":  {"جداسازی", "unbind"},
	"devices.block":   {"قطع", "block"},
	"devices.unblock": {"رفع قطع", "unblock"},
	"devices.delete":  {"حذف", "delete"},
	"devices.delete_confirm": {"این دستگاه و همه سابقه‌اش حذف شود؟",
		"Delete this device and all its history?"},
	"devices.deleted_ok": {"دستگاه حذف شد", "device deleted"},
	"devices.renamed_ok": {"نام دستگاه ذخیره شد", "device name saved"},
	"devices.rename":     {"ذخیره نام", "save name"},
	"devices.edit_name":  {"ویرایش نام", "edit name"},
	"devices.label_ph":   {"نام نمایشی", "display name"},
	"devices.ok":         {"سالم", "ok"},
	"devices.count":      {"دستگاه", "devices"},
	"devices.online":     {"آنلاین", "online"},
	"devices.blocked":    {"قطع", "blocked"},
	"devices.all":        {"همه", "all"},
	"devices.manage":     {"مدیریت دستگاه", "manage device"},
	"devices.owner_hint": {"تغییر مالک بلافاصله ذخیره می‌شود؛ «مهمان» یعنی بدون مالک.", "Changing the owner saves right away; “guest” means no owner."},
	"devices.last_used":  {"آخرین استفاده", "last used"},
	"devices.mac":        {"مک‌آدرس", "MAC"},
	"devices.cancel":     {"انصراف", "cancel"},
	"devices.search_ph":  {"جستجوی نام، مالک یا IP", "search name, owner or IP"},
	"devices.filter":     {"فیلتر دستگاه‌ها", "filter devices"},
	"devices.empty":      {"هنوز دستگاهی ثبت نشده", "No devices yet"},
	"devices.empty_hint": {"دستگاه‌ها به‌محض اتصال به شبکه اینجا ظاهر می‌شوند.", "Devices appear here once they join the network."},
	"devices.no_match":   {"دستگاهی با این فیلتر پیدا نشد.", "No device matches this filter."},

	"domestic.title":     {"سایت‌های نیم‌بها", "Domestic (half-price) sites"},
	"domestic.refresh":   {"بروزرسانی لیست", "Refresh list"},
	"domestic.refreshed": {"لیست بروزرسانی شد", "list updated"},
	"domestic.last":      {"آخرین بروزرسانی:", "last refresh:"},
	"domestic.never":     {"هنوز دریافت نشده", "never"},
	"domestic.rows":      {"سایت", "sites"},
	"domestic.search":    {"جستجو", "search"},
	"domestic.search_ph": {"دامنه یا شبکه", "domain or network"},
	"domestic.prev":      {"قبلی", "prev"},
	"domestic.next":      {"بعدی", "next"},
	"domestic.empty":     {"لیستی ذخیره نشده است. دکمه بروزرسانی را بزنید.", "No list stored yet. Press refresh."},
	"domestic.no_match":  {"موردی پیدا نشد.", "No match."},
	"domestic.hint":      {"لیست رسمی تعرفه داخلی سازمان فناوری اطلاعات. دریافت آن حدود ۱۵ ثانیه طول می‌کشد و جای لیست قبلی را می‌گیرد.", "The official ITO domestic-tariff list. Fetching takes about 15 seconds and replaces the previous list."},

	"routes.title":       {"مسیرها", "Routes"},
	"routes.hint":        {"مسیر اینجا تمام می‌شود. تغییر خروجی هیچ‌گاه اشتراک را عوض نمی‌کند: کلاینت‌ها همیشه به همین باکس وصل می‌شوند. upstream_json همان شیء outbound سینگ‌باکس است (type، server، server_port، …).", "A route terminates here. Changing an upstream never changes a subscription: clients always point at this box. upstream_json is the sing-box outbound object (type, server, server_port, …)."},
	"routes.rate_hint":   {"نرخ هر دسته ضریب محاسبه است: ۱ یعنی تمام، ۰٫۳ یعنی ۳۰٪. یک‌بار و در اولین دسته‌بندی هر اتصال اعمال می‌شود.", "Each class rate is the billing multiplier: 1 = full price, 0.3 = pay 30%. It is applied once per connection, at first classification."},
	"routes.rates":       {"نرخ دسته‌ها", "class rates"},
	"routes.all_persons": {"همه افراد", "all persons"},
	"routes.enabled":     {"فعال", "enabled"},
	"routes.new_ph":      {"مسیر جدید", "new route"},
	"routes.add":         {"افزودن مسیر", "add route"},
	"routes.save":        {"ذخیره", "save"},

	"period.pool":         {"سهمیه دوره (GB)", "pool charged (GB)"},
	"period.pool_ph":      {"خالی = فقط اندازه‌گیری", "empty = meter only"},
	"period.guest_pool":   {"استخر مهمان (GB)", "guest pool (GB)"},
	"period.guest_device": {"سهمیه هر مهمان (GB)", "guest device (GB)"},
	"period.save":         {"ذخیره تنظیمات دوره", "save period settings"},

	"usage.title": {"مصرف", "Usage"},
	"audit.title": {"گزارش رویدادها", "Audit log"},

	"machine.title": {"رابط ماشینی", "Machine interface"},
	"machine.hint":  {"همین اعداد به‌صورت JSON. کلید بایپس:", "the same numbers as JSON. Bypass switch:"},

	"update.title":         {"بروزرسانی", "Update"},
	"update.hint":          {"آخرین نسخه را از گیت‌هاب می‌گیرد، دوباره می‌سازد و سرویس را راه‌اندازی می‌کند. چند ثانیه اینترنت قطع می‌شود و باید دوباره وارد شوید.", "Pulls the latest commit from GitHub, rebuilds and restarts the service. Internet drops for a few seconds and you must sign in again."},
	"update.run":           {"بروزرسانی", "Update now"},
	"update.confirm":       {"بروزرسانی انجام شود؟ سرویس برای چند ثانیه قطع می‌شود.", "Run the update? The service restarts for a few seconds."},
	"update.requested":     {"بروزرسانی شروع شد", "update started"},
	"update.last":          {"آخرین بروزرسانی:", "last update:"},
	"update.state.running": {"در حال اجرا", "running"},
	"update.state.ok":      {"موفق", "ok"},
	"update.state.failed":  {"ناموفق", "failed"},
}

func T(l, key string) string {
	pair, ok := messages[key]
	if !ok {
		return key
	}
	if l == "en" {
		return pair[1]
	}
	return pair[0]
}

var faDigits = []string{"۰", "۱", "۲", "۳", "۴", "۵", "۶", "۷", "۸", "۹"}

func persianDigits(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteString(faDigits[c-'0'])
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Bytes renders a byte count at household scale. Persian gets Persian digits.
func Bytes(n int64, l string) string {
	if n < 0 {
		return "—"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	var s string
	if v >= 100 || i == 0 {
		s = fmt.Sprintf("%.0f %s", v, units[i])
	} else {
		s = fmt.Sprintf("%.1f %s", v, units[i])
	}
	if l == "fa" {
		return persianDigits(strings.ReplaceAll(s, ".", "٫"))
	}
	return s
}

// percent rounds the share up, so a usage percentage never reads lower than
// the bytes behind it.
func percent(used, cap int64) int {
	if cap <= 0 {
		return 0
	}
	p := int(math.Ceil(float64(used) / float64(cap) * 100))
	if p > 100 {
		p = 100
	}
	return p
}

func funcs(loc *time.Location) template.FuncMap {
	return template.FuncMap{
		"T":          T,
		"reason":     reasonText,
		"bytes":      Bytes,
		"pct":        percent,
		"deviceName": deviceLabel,
		"jalali": func(t int64, style string) string {
			return jalali.Format(time.Unix(t, 0), style, loc)
		},
		"jalaliTime": func(t time.Time, style string) string {
			return jalali.Format(t, style, loc)
		},
		"upper": strings.ToUpper,
		"digits": func(n int, l string) string {
			s := strconv.Itoa(n)
			if l == "fa" {
				return persianDigits(s)
			}
			return s
		},
	}
}
