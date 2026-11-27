# 9. Data model

SQLite in WAL mode, one file, backed up by copying.

```sql
person(id, name, weight, cap_override, enabled, sub_token, created_at, locale)
device(mac PK, ip, hostname, name, first_seen, last_seen, last_used,
       owner_person_id, bound_at, bind_source)

route(id, name, kind, upstream_json, allow_all, enabled)
credential(id, person_id, route_id, uuid, port, enabled)
                                              -- one listener per (person, route)

class(id, name)                               -- default, nimbaha
rate(route_id, class_id, weight)              -- billing multiplier, default 1.0

period(id, start_utc, end_utc, anchor_jalali_day,
       pool_charged, guest_pool, raw_ceiling, closed_at)
usage(period_id, subject, route_id, class_id,
      bytes_up, bytes_down, charged,
      PRIMARY KEY(period_id, subject, route_id, class_id))
block(subject, until, reason, created_by)
event(at, kind, subject, detail)
```

`subject` is the string `person:<id>` or `dev:<mac>` — one column, no union
type, easy to group.

Notes:

- `usage` is upserted per 5-minute flush, not per poll. The poll only touches
  memory; the DB sees ~288 writes per subject per day.
- `mac` is stored normalized (`aa:bb:cc:dd:ee:ff`).
- `hostname` is DHCP option 12 as the client sent it; `name` is what the device
  advertises over mDNS. The UI shows `name`, then a prettified `hostname`, then
  the MAC. A name is never invented: when a phone advertises neither, the MAC
  stays.
- `last_seen` is the lease poll, stamped for every device still in the lease
  file, so it cannot tell a device that just joined from one that has been here
  all week. `last_used` is when the accountant last booked bytes for it, written
  with the usage flush; the Devices tab sorts by the later of `last_used` and
  `first_seen`.
- Every quota-affecting change writes an `event` row. The audit log is the
  same table.
- Schema migrations are numbered SQL files applied at startup.
