-- field: updatePostById
WITH __mut AS (
  UPDATE "public"."posts"
  SET "body" = $1
  WHERE "public"."posts"."id" = $2
  RETURNING *
)
SELECT jsonb_build_object('post', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'excerpt', "public"."posts_excerpt"(ROW(__t_1."id", __t_1."author_id", __t_1."title", __t_1."body", __t_1."published")::"public"."posts", $3::"int4")) AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["Updated body text",1,5]

-- field: createUser
WITH __mut AS (
  INSERT INTO "public"."users" ("email")
  VALUES ($1)
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'postCount', ("public"."users_post_count"(ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses")::"public"."users"))::text) AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["new@example.com"]

