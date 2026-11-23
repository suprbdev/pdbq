-- field: allPosts
SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_3
WHERE __c_3."author_id" = $4)::int, 'aggregates', (SELECT json_build_object('__typename', 'PostAggregates'::text, 'sum', json_build_object('id', (coalesce(sum(__a_4."id"), '0'))::text, 'authorId', (coalesce(sum(__a_4."author_id"), '0'))::text), 'distinctCount', json_build_object('title', (count(DISTINCT __a_4."title"))::text), 'average', json_build_object('id', (avg(__a_4."id"))::text), 'max', json_build_object('id', max(__a_4."id")), 'stddevSample', json_build_object('id', (stddev_samp(__a_4."id"))::text), 'variancePopulation', json_build_object('authorId', (var_pop(__a_4."author_id"))::text)) FROM "public"."posts" AS __a_4
WHERE __a_4."author_id" = $5), 'nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."author_id" = $1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: [1,2,1,1,1]

-- field: allUsers
SELECT json_build_object('a', (SELECT json_build_object('min', json_build_object('createdAt', min(__a_7."created_at"), 'balance', (min(__a_7."balance"))::text), 'sum', json_build_object('balance', (coalesce(sum(__a_7."balance"), '0'))::text), 'keys', NULL::json) FROM "public"."users" AS __a_7
WHERE __a_7."mood" = $7), 'nodes', coalesce(json_agg(__s_6.ndata ORDER BY __s_6.__rn ASC) FILTER (WHERE __s_6.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('email', __t_1."email", 'postsByAuthorId', __l_5.data) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('aggregates', (SELECT json_build_object('sum', json_build_object('id', (coalesce(sum(__a_4."id"), '0'))::text), 'distinctCount', json_build_object('id', (count(DISTINCT __a_4."id"))::text)) FROM "public"."posts" AS __a_4
    WHERE __a_4."author_id" = __t_1."id" AND __a_4."title" LIKE $3)) AS data
    FROM (
      SELECT row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM "public"."posts" AS __t_2
      WHERE __t_2."author_id" = __t_1."id" AND __t_2."title" LIKE $1
      ORDER BY __t_2."id" ASC
      LIMIT $2
    ) AS __s_3
    GROUP BY ()
  ) AS __l_5 ON true
  WHERE __t_1."mood" = $4
  ORDER BY __t_1."id" ASC
  LIMIT $5
) AS __s_6
GROUP BY ()
-- args: ["N%",2,"N%","happy",101,100,"happy"]

