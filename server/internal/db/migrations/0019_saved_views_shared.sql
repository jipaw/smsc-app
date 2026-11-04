-- Fase 39: tampilan transaksi dapat dibagikan ke seluruh tim (hanya-baca bagi pengguna lain). Tampilan berbagi tidak boleh memuat
-- filter nomor ("to"): viewer melihat nomor termasking, jadi awalan nomor di filter akan membocorkannya.
BEGIN;
ALTER TABLE saved_views ADD COLUMN shared boolean NOT NULL DEFAULT false;
ALTER TABLE saved_views ADD CONSTRAINT saved_views_shared_no_number CHECK (NOT shared OR COALESCE(filter->>'to', '') = '');
CREATE INDEX saved_views_shared_idx ON saved_views (lower(name)) WHERE shared;
COMMIT;
