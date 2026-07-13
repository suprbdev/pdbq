-- field: upsertUserByEmail
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data, 'clientMutationId', $3::text) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'email', __t_1."email", 'fullName', __t_1."full_name") AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["a@b.c","Ada","cm-1"]

-- field: upsertUserByEmail
WITH __mut AS (
  INSERT INTO "public"."users" ("email")
  VALUES ($1)
  ON CONFLICT ("email") DO UPDATE
  SET "email" = EXCLUDED."email"
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["a@b.c"]

