-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE NOT (coalesce((SELECT max(__r_2."id") IS NULL FROM "public"."places" AS __r_2 WHERE __r_2."owner_id" = __t_1."id"), false)) AND coalesce((SELECT count(DISTINCT __r_3."title") = ANY($1) AND coalesce(sum(__r_3."id"), '0') > $3 FROM "public"."posts" AS __r_3 WHERE __r_3."author_id" = __t_1."id" AND (__r_3."title" <> $2)), false) AND EXISTS (SELECT 1 FROM "public"."posts" AS __r_4 WHERE __r_4."author_id" = __t_1."id" AND (__r_4."title" LIKE $4))
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_5
GROUP BY ()
-- args: [["1","2"],"Unpublished draft","2","N%",100]

