-- field: searchPosts
SELECT coalesce(jsonb_agg(__s_4.data), '[]'::jsonb) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'title', __t_1."title", 'author', __l_3.data) AS data
  FROM "public"."search_posts"($1::"text") AS __t_1
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('email', __t_2."email") AS data
    FROM "public"."users" AS __t_2
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_3 ON true
) AS __s_4
-- args: ["graphql"]

