-- Identitas node tailnet device (hasil WhoIs saat enroll lewat listener tsnet) dan id auth key Tailscale yang dibuat untuk enrollment.
BEGIN;
ALTER TABLE devices ADD COLUMN tailnet_node_id text;
ALTER TABLE devices ADD COLUMN tailnet_node_name text;
ALTER TABLE devices ADD COLUMN tailnet_tags text[];
ALTER TABLE devices ADD COLUMN tailnet_verified_at timestamptz;
ALTER TABLE device_enrollments ADD COLUMN tailnet_key_id text;
COMMIT;
