-- The name the device advertises over mDNS. hostname stays the DHCP option 12
-- value; the UI prefers name, because "Ali’s MacBook Pro" is only on mDNS.
ALTER TABLE device ADD COLUMN name TEXT;
