# 7. Periods and the Jalali calendar

The ISP does not reset on the first of any month, Jalali or Gregorian. The
reset day is an **admin-chosen Jalali day-of-month**, recurring every Jalali
month.

```yaml
period:
  anchor_jalali_day: 12        # 1..31, required
  anchor_start: 1405-08-12     # start of the period running now (Jalali)
  timezone: Asia/Tehran        # fixed +03:30 since 2022, no DST
  pool_charged: null           # GB; null = meter but never block
  guest_pool: 5                # GB carved out for guest devices
  raw_ceiling: null            # ISP's real quota, set a few % below it
```

## 7.1 Boundary algorithm

Boundaries are computed **from the anchor, never chained from the previous
(clamped) boundary**, or the anchor day drifts down after every short month:

```
boundary(k) = jalali(anchor.year, anchor.month + k, min(anchor.day, daysIn(month)))
```

- Jalali months 1–6 have 31 days, 7–11 have 30, Esfand has 29 or 30.
- Anchor day 31 in a 30-day month clamps to the 30th **for that month only**;
  the following month goes back to the 31st.
- Boundary instant is 00:00 Asia/Tehran, stored as a UTC timestamp.
- Never add "30 days" and never assume a fixed month length.

Worked example, anchor 12:

```
1405/08/12 → 1405/09/12 → 1405/10/12 → 1405/11/12 → 1405/12/12 → 1406/01/12
anchor 31: 1405/05/31 → 1405/06/31 → 1405/07/30 (clamped) → 1405/08/31
```

Implementation rules:

- **UTC timestamps are the source of truth.** Jalali strings are a rendering
  layer; `1405/09/12` and `12 Azar` are the same value formatted differently.
- Use `github.com/jalaali/go-jalaali` (pin the version). Do not hand-roll leap
  years.
- One conversion helper used by every surface, plus
  `formatJalali(t, "short"|"long"|"numeric")`.
- A new period is a **new row**, so "reset" deletes nothing, history is free,
  and a mistake is recoverable. Usage rows carry `period_id`.
- A systemd timer at 00:05 Tehran opens the next period (and backfills if the
  box was off during the boundary). Opening a period is idempotent.
- Changing the anchor mid-period closes the current period immediately and
  opens a new one from the change instant; old usage keeps its old period.
- Unit tests: Esfand in a leap and a non-leap year, anchor 31, anchor 1, and a
  period that spans Nowruz.
