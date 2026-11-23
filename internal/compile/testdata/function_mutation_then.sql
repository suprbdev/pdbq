-- field: publishPost
SELECT json_build_object('clientMutationId', $2::text, 'post', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('id', __t_1."id", 'title', __t_1."title", 'nodeId', replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '')) END, 'postEdge', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('cursor', replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'node', json_build_object('id', __t_1."id")) END) AS data
FROM "public"."publish_post"("post_id" => $1::"pg_catalog"."int4") AS __t_1
-- args: [7,"cm"]

-- field: unpublishPost
SELECT coalesce(json_agg(__t_1::text), '[]'::json) AS data
FROM "public"."unpublish_post"("post_id" => $1::"pg_catalog"."int4") AS __t_1
-- args: [1]
-- then:
SELECT json_build_object('postEdge', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('node', json_build_object('userByAuthorId', __l_4.data)) END) AS data
FROM json_array_elements_text($1::json) AS __r_2(v)
CROSS JOIN LATERAL (SELECT (__r_2.v::"public"."posts").*) AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_3."email") AS data
  FROM "public"."users" AS __t_3
  WHERE __t_3."id" = __t_1."author_id"
  LIMIT 1
) AS __l_4 ON true
-- args: []

-- field: addNumbers
SELECT json_build_object('integer', to_json(__fn.v), 'again', to_json(__fn.v)) AS data
FROM (SELECT "public"."add_numbers"("a" => $1::"pg_catalog"."int4", "b" => $2::"pg_catalog"."int4") AS v) AS __fn
-- args: [1,2]
-- then:
SELECT json_build_object('integer', ($1::json -> 'integer'), 'again', ($1::json -> 'again'), 'clientMutationId', $2::text, 'query', json_build_object('allPosts', (SELECT __q_4.data FROM (
  SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_3)::int) AS data
  FROM (
    SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
    FROM "public"."posts" AS __t_1
    ORDER BY __t_1."id" ASC
    LIMIT 2
  ) AS __s_2
  GROUP BY ()
) AS __q_4))) AS data
-- args: ["cm"]

-- field: publishPosts
SELECT coalesce(json_agg(__t_1::text), '[]'::json) AS data
FROM "public"."publish_posts"("post_ids" => $1::"pg_catalog"."_int4") AS __t_1
-- args: [[1,2]]
-- then:
SELECT json_build_object('first', (SELECT coalesce(json_agg(__s_3.data ORDER BY __s_3.n), '[]'::json) FROM (
  SELECT json_build_object('id', __t_1."id") AS data, __r_2.n
  FROM json_array_elements_text($1::json) WITH ORDINALITY AS __r_2(v, n)
  CROSS JOIN LATERAL (SELECT (__r_2.v::"public"."posts").*) AS __t_1
) AS __s_3), 'second', (SELECT coalesce(json_agg(__s_6.data ORDER BY __s_6.n), '[]'::json) FROM (
  SELECT json_build_object('title', __t_4."title") AS data, __r_5.n
  FROM json_array_elements_text($1::json) WITH ORDINALITY AS __r_5(v, n)
  CROSS JOIN LATERAL (SELECT (__r_5.v::"public"."posts").*) AS __t_4
) AS __s_6), 'query', json_build_object('postById', (SELECT __q_8.data FROM (
  SELECT json_build_object('title', __t_7."title") AS data
  FROM "public"."posts" AS __t_7
  WHERE __t_7."id" = $2
  LIMIT 1
) AS __q_8))) AS data
-- args: [1]

-- field: bumpMood
SELECT json_build_object('mood', to_json(CASE __fn.v WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END), 'query', json_build_object('__typename', 'Query'::text)) AS data
FROM (SELECT "public"."bump_mood"("m" => $1::"public"."mood") AS v) AS __fn
-- args: ["happy"]

-- field: touchPost
SELECT '{}'::json AS data
FROM (SELECT "public"."touch_post"("post_id" => $1::"pg_catalog"."int4") AS v) AS __fn
-- args: [1]
-- then:
SELECT json_build_object('query', json_build_object('__typename', 'Query'::text, 'userById', (SELECT __q_2.data FROM (
  SELECT json_build_object('email', __t_1."email") AS data
  FROM "public"."users" AS __t_1
  WHERE __t_1."id" = $2
  LIMIT 1
) AS __q_2))) AS data
-- args: [1]

