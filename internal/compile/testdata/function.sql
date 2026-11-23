-- field: searchPosts
SELECT coalesce(json_agg(__s_4.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'title', __t_1."title", 'userByAuthorId', __l_3.data) AS data
  FROM "public"."search_posts"("term" => $1::"pg_catalog"."text") AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', __t_2."email") AS data
    FROM "public"."users" AS __t_2
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_3 ON true
) AS __s_4
-- args: ["graphql"]

