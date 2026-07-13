-- field: allPosts
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3), '[]'::jsonb), 'pageInfo', jsonb_build_object('hasNextPage', (count(*) > $3), 'endCursor', (jsonb_agg(__s_3.__cursor ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3))->-1)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'title', __t_1."title") AS ndata, replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."title" ASC, __t_1."author_id" DESC, __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  CROSS JOIN (
    SELECT __a."title" AS k1, __a."author_id" AS k2, __a."id" AS k3
    FROM "public"."posts" AS __a
    WHERE __a."id" = $1
  ) AS __anc_2
  WHERE (__t_1."title" > __anc_2.k1 OR (__t_1."title" = __anc_2.k1 AND (__t_1."author_id" < __anc_2.k2 OR (__t_1."author_id" = __anc_2.k2 AND __t_1."id" > __anc_2.k3))))
  ORDER BY __t_1."title" ASC, __t_1."author_id" DESC, __t_1."id" ASC
  LIMIT $2
) AS __s_3
GROUP BY ()
-- args: ["3",4,3]

