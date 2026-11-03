-- Fase 13: grup SCIM untuk pemetaan peran.
BEGIN;
ALTER TABLE users ADD COLUMN scim_direct_role user_role;   -- peran dari atribut roles (NULL = tidak diberikan); peran efektif = tertinggi(langsung, grup)
UPDATE users SET scim_direct_role = role WHERE scim_managed;

CREATE TABLE scim_groups (
  id           uuid PRIMARY KEY,
  display_name text NOT NULL,
  external_id  text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX scim_groups_name_uq ON scim_groups (lower(display_name));
CREATE UNIQUE INDEX scim_groups_ext_uq  ON scim_groups (external_id) WHERE external_id IS NOT NULL;

CREATE TABLE scim_group_members (
  group_id uuid NOT NULL REFERENCES scim_groups(id) ON DELETE CASCADE,
  user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX scim_group_members_user_idx ON scim_group_members (user_id);
COMMIT;
