-- field: updatePostById
WITH __mut AS (
  UPDATE "public"."posts"
  SET "body" = $1
  WHERE "public"."posts"."id" = $2
  RETURNING "id", "author_id", "title", "body", "published"
)
SELECT json_build_object('post', json_build_object('id', __t_1."id", 'excerpt', "public"."posts_excerpt"("p" => ROW(__t_1."id", __t_1."author_id", __t_1."title", __t_1."body", __t_1."published")::"public"."posts", "max_chars" => $3::"pg_catalog"."int4"))) AS data
FROM __mut AS __t_1
-- args: ["Updated body text",1,5]

-- field: createUser
WITH __mut AS (
  INSERT INTO "public"."users" ("email")
  VALUES ($1)
  RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"
)
SELECT json_build_object('user', json_build_object('id', __t_1."id", 'postCount', ("public"."users_post_count"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users"))::text)) AS data
FROM __mut AS __t_1
-- args: ["new@example.com"]

