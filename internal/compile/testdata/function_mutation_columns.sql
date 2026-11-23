-- field: publishPosts
SELECT json_build_object('posts', __agg.data) AS data
FROM (SELECT coalesce(json_agg(__s_2.data), '[]'::json) AS data FROM (
  SELECT json_build_object('id', __t_1."id", 'title', __t_1."title") AS data
  FROM "public"."publish_posts"("post_ids" => $1::"pg_catalog"."_int4") AS __t_1
) AS __s_2) AS __agg
-- args: [[1,2]]

