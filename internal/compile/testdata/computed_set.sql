-- field: userById
SELECT jsonb_build_object('email', __t_1."email", 'tagWords', (SELECT coalesce(jsonb_agg(__v_2.v), '[]'::jsonb) FROM "public"."users_tag_words"(ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses")::"public"."users") AS __v_2(v)), 'recentPosts', (SELECT coalesce(jsonb_agg(__s_6.data), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('title', __t_3."title", 'author', __l_5.data) AS data
  FROM "public"."users_recent_posts"(ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses")::"public"."users", $1::"int4") AS __t_3
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('email', __t_4."email") AS data
    FROM "public"."users" AS __t_4
    WHERE __t_4."id" = __t_3."author_id"
    LIMIT 1
  ) AS __l_5 ON true
) AS __s_6)) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $2
LIMIT 1
-- args: [3,1]

