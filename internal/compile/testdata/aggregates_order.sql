-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY (SELECT count(*) FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") DESC NULLS LAST, (SELECT coalesce(sum(__ro."id"), '0') FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") ASC, __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY (SELECT count(*) FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") DESC NULLS LAST, (SELECT coalesce(sum(__ro."id"), '0') FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") ASC, __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [100]

-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json), 'pageInfo', json_build_object('hasNextPage', (count(*) > $3))) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, replace(encode(convert_to(jsonb_build_array('User'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY (SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE (((SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") < (SELECT (SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __a."id") FROM "public"."users" AS __a WHERE __a."id" = $1) OR ((SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") IS NULL AND (SELECT (SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __a."id") FROM "public"."users" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR ((SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") IS NOT DISTINCT FROM (SELECT (SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __a."id") FROM "public"."users" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2))
  ORDER BY (SELECT avg(__ro."id") FROM "public"."posts" AS __ro WHERE __ro."author_id" = __t_1."id") DESC NULLS LAST, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["1","1",2]

