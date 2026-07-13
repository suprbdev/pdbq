-- field: addNumbers
SELECT jsonb_build_object('__typename', 'AddNumbersPayload'::text, 'clientMutationId', $3::text, 'result', to_jsonb(__fn.v)) AS data
FROM (SELECT "public"."add_numbers"($1::"int4", $2::"int4") AS v) AS __fn
-- args: [2,40,"cm-1"]

-- field: publishPost
SELECT jsonb_build_object('result', CASE WHEN __t_1 IS NULL THEN NULL ELSE jsonb_build_object('title', __t_1."title", 'author', __l_3.data) END) AS data
FROM "public"."publish_post"($1::"int4") AS __t_1
LEFT JOIN LATERAL (
  SELECT jsonb_build_object('email', __t_2."email") AS data
  FROM "public"."users" AS __t_2
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_3 ON true
-- args: [7]

-- field: retryProbe
SELECT jsonb_build_object('clientMutationId', NULL::text) AS data
FROM (SELECT "public"."retry_probe"() AS v) AS __fn
-- args: null

