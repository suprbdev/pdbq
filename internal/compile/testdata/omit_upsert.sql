-- field: upsertUserByEmail
WITH __mut AS (
  INSERT INTO "public"."users" ("balance", "email", "full_name")
  VALUES ($1, $2, $3)
  ON CONFLICT ("email") DO UPDATE
  SET "balance" = EXCLUDED."balance"
  RETURNING "id", "email", "full_name"
)
SELECT json_build_object('user', json_build_object('id', __t_1."id", 'email', __t_1."email", 'fullName', __t_1."full_name")) AS data
FROM __mut AS __t_1
-- args: ["5","a@b.c","Ada"]

-- field: upsertUserByEmail
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "email" = EXCLUDED."email"
  RETURNING "id"
)
SELECT json_build_object('user', json_build_object('id', __t_1."id")) AS data
FROM __mut AS __t_1
-- args: ["a@b.c","Ada"]

