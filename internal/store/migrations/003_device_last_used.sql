-- When the accountant last booked bytes for this device. last_seen is the lease
-- poll and moves for every device still in the lease file, so it cannot answer
-- "which device was just used".
ALTER TABLE device ADD COLUMN last_used INTEGER;
