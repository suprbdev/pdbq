-- field: userById
SELECT json_build_object('email', __t_1."email", 'tagWords', (SELECT coalesce(json_agg(__v_2.v), '[]'::json) FROM (SELECT * FROM "public"."users_tag_words"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users") AS __v_2(v) LIMIT 100) AS __v_2), 'recentPosts', (SELECT coalesce(json_agg(__s_6.data), '[]'::json) FROM (
  SELECT json_build_object('title', __t_3."title", 'userByAuthorId', __l_5.data) AS data
  FROM "public"."users_recent_posts"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users", "n" => $1::"pg_catalog"."int4") AS __t_3
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', __t_4."email") AS data
    FROM "public"."users" AS __t_4
    WHERE __t_4."id" = __t_3."author_id"
    LIMIT 1
  ) AS __l_5 ON true
  LIMIT 100
) AS __s_6)) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $2
LIMIT 1
-- args: [3,1]

